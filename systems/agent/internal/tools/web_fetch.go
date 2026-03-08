// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	tableplugin "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	webFetchModeAuto    = "auto"
	webFetchModeArticle = "article"
	webFetchModePage    = "page"

	defaultWebFetchMaxChars     = 12000
	minWebFetchMaxChars         = 1000
	maxWebFetchMaxChars         = 24000
	defaultWebFetchTimeout      = 15 * time.Second
	defaultWebFetchResponseSize = 4 << 20
	defaultWebFetchCacheTTL     = 5 * time.Minute
	defaultWebFetchCacheEntries = 32
	webFetchUserAgent           = "q15-web-fetch/1.0"
)

var webFetchExtraBlankLines = regexp.MustCompile(`\n{3,}`)

// WebFetch fetches HTML pages and returns cleaned markdown slices with metadata.
type WebFetch struct {
	client           *http.Client
	cache            *webFetchCache
	userAgent        string
	maxResponseBytes int64
}

type webFetchDocument struct {
	requestedURL string
	finalURL     string
	title        string
	source       string
	site         string
	byline       string
	published    string
	markdown     string
}

type webFetchCache struct {
	mu         sync.Mutex
	now        func() time.Time
	ttl        time.Duration
	maxEntries int
	entries    map[string]webFetchCacheEntry
}

type webFetchCacheEntry struct {
	storedAt time.Time
	document webFetchDocument
}

// NewWebFetch constructs a web fetch tool with bounded network and cache settings.
func NewWebFetch() *WebFetch {
	return &WebFetch{
		client: &http.Client{Timeout: defaultWebFetchTimeout},
		cache: newWebFetchCache(
			defaultWebFetchCacheTTL,
			defaultWebFetchCacheEntries,
			time.Now,
		),
		userAgent:        webFetchUserAgent,
		maxResponseBytes: defaultWebFetchResponseSize,
	}
}

// Definition returns the tool schema exposed to the model.
func (w *WebFetch) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch a known web page URL, convert readable HTML content to markdown, and return a bounded slice with fetch metadata.",
		PromptGuidance: []string{
			"Use for known HTTP or HTTPS URLs when you need page content.",
			"Prefer this over shelling out with curl for ordinary webpage reads.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "HTTP or HTTPS URL to fetch",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "Fetch mode: auto prefers article extraction and falls back to cleaned page conversion; article requires readable article content; page skips article extraction",
					"enum": []string{
						webFetchModeAuto,
						webFetchModeArticle,
						webFetchModePage,
					},
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Rune offset for a follow-up slice of the converted markdown",
					"minimum":     0,
				},
				"max_chars": map[string]any{
					"type":        "integer",
					"description": "Maximum number of characters to return in this slice (clamped to 1000-24000)",
					"minimum":     minWebFetchMaxChars,
					"maximum":     maxWebFetchMaxChars,
				},
			},
			"required": []string{"url"},
		},
	}
}

// Run executes one web fetch request from raw JSON tool arguments.
func (w *WebFetch) Run(ctx context.Context, arguments string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("web fetch tool is not configured")
	}
	if w.client == nil {
		return "", fmt.Errorf("web fetch http client is not configured")
	}

	var args struct {
		URL      string `json:"url"`
		Mode     string `json:"mode"`
		Offset   *int   `json:"offset"`
		MaxChars *int   `json:"max_chars"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}

	requestedURL := strings.TrimSpace(args.URL)
	if requestedURL == "" {
		return "", fmt.Errorf("missing required argument: url")
	}

	mode, err := normalizeWebFetchMode(args.Mode)
	if err != nil {
		return "", err
	}

	offset := 0
	if args.Offset != nil {
		offset = *args.Offset
	}
	if offset < 0 {
		return "", fmt.Errorf("offset must be >= 0")
	}

	maxChars := defaultWebFetchMaxChars
	if args.MaxChars != nil {
		maxChars = clamp(*args.MaxChars, minWebFetchMaxChars, maxWebFetchMaxChars)
	}

	cacheKey := requestedURL + "|" + mode
	if w.cache != nil {
		if document, ok := w.cache.Get(cacheKey); ok {
			return renderWebFetchDocument(document, offset, maxChars)
		}
	}

	document, err := w.fetchDocument(ctx, requestedURL, mode)
	if err != nil {
		return "", err
	}
	if w.cache != nil {
		w.cache.Set(cacheKey, document)
	}

	return renderWebFetchDocument(document, offset, maxChars)
}

func (w *WebFetch) fetchDocument(
	ctx context.Context,
	requestedURL string,
	mode string,
) (webFetchDocument, error) {
	parsedURL, err := parseWebFetchURL(requestedURL)
	if err != nil {
		return webFetchDocument{}, err
	}

	rawBody, contentType, finalURL, err := w.fetchHTML(ctx, parsedURL.String())
	if err != nil {
		return webFetchDocument{}, err
	}

	decodedBody, err := charset.NewReader(bytes.NewReader(rawBody), contentType)
	if err != nil {
		return webFetchDocument{}, fmt.Errorf("decode HTML response: %w", err)
	}

	doc, err := html.Parse(decodedBody)
	if err != nil {
		return webFetchDocument{}, fmt.Errorf("parse HTML response: %w", err)
	}

	pageTitle := extractDocumentTitle(doc)

	switch mode {
	case webFetchModeArticle:
		parser := readability.NewParser()
		if !parser.CheckDocument(doc) {
			return webFetchDocument{}, fmt.Errorf("no readable article content found")
		}
		return buildArticleWebFetchDocument(doc, finalURL, requestedURL, pageTitle)
	case webFetchModePage:
		return buildPageWebFetchDocument(doc, finalURL, requestedURL, pageTitle)
	default:
		parser := readability.NewParser()
		if parser.CheckDocument(doc) {
			document, err := buildArticleWebFetchDocument(doc, finalURL, requestedURL, pageTitle)
			if err == nil {
				return document, nil
			}
		}
		return buildPageWebFetchDocument(doc, finalURL, requestedURL, pageTitle)
	}
}

func parseWebFetchURL(rawURL string) (*url.URL, error) {
	parsedURL, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(parsedURL.Scheme)) {
	case "http", "https":
		return parsedURL, nil
	default:
		return nil, fmt.Errorf(
			"unsupported url scheme %q: only http and https are allowed",
			parsedURL.Scheme,
		)
	}
}

func normalizeWebFetchMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return webFetchModeAuto, nil
	}

	switch mode {
	case webFetchModeAuto, webFetchModeArticle, webFetchModePage:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid mode %q: must be one of auto, article, page", mode)
	}
}

func (w *WebFetch) fetchHTML(
	ctx context.Context,
	requestURL string,
) ([]byte, string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", nil, fmt.Errorf("create web fetch request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	userAgent := strings.TrimSpace(w.userAgent)
	if userAgent == "" {
		userAgent = webFetchUserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := w.client.Do(req)
	if err != nil {
		if isTimeoutError(err) {
			return nil, "", nil, fmt.Errorf("web fetch request timed out: %w", err)
		}
		return nil, "", nil, fmt.Errorf("web fetch request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, w.maxResponseBytes+1))
	if err != nil {
		return nil, "", nil, fmt.Errorf("read web fetch response: %w", err)
	}
	if int64(len(body)) > w.maxResponseBytes {
		return nil, "", nil, fmt.Errorf("web fetch response exceeded %d bytes", w.maxResponseBytes)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", nil, fmt.Errorf(
			"web fetch request returned %s: %s",
			resp.Status,
			trimForError(string(body), maxErrorResponseBody),
		)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isHTMLContentType(contentType) && !isHTMLContentType(http.DetectContentType(body)) {
		if contentType == "" {
			contentType = "(missing)"
		}
		return nil, "", nil, fmt.Errorf(
			"web fetch response is not an HTML document (Content-Type: %s)",
			contentType,
		)
	}

	finalURL := resp.Request.URL
	if finalURL == nil {
		finalURL, _ = url.Parse(requestURL)
	}

	return body, contentType, finalURL, nil
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isHTMLContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(contentType))
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/html", "application/xhtml+xml":
		return true
	default:
		return false
	}
}

func buildArticleWebFetchDocument(
	doc *html.Node,
	finalURL *url.URL,
	requestedURL string,
	pageTitle string,
) (webFetchDocument, error) {
	parser := readability.NewParser()
	article, err := parser.ParseDocument(doc, finalURL)
	if err != nil {
		return webFetchDocument{}, fmt.Errorf("extract readable article: %w", err)
	}
	if article.Node == nil {
		return webFetchDocument{}, fmt.Errorf("no readable article content found")
	}

	markdown, err := convertNodeToMarkdown(article.Node, finalURL.String())
	if err != nil {
		return webFetchDocument{}, fmt.Errorf("convert article HTML to markdown: %w", err)
	}
	markdown = normalizeFetchedMarkdown(markdown)
	if markdown == "" {
		return webFetchDocument{}, fmt.Errorf("extracted content was empty")
	}

	title := normalizeMetadata(article.Title())
	if title == "" {
		title = normalizeMetadata(pageTitle)
	}
	if title == "" {
		title = "(untitled)"
	}

	published := ""
	if publishedTime, err := article.PublishedTime(); err == nil {
		published = publishedTime.Format(time.RFC3339)
	}

	return webFetchDocument{
		requestedURL: requestedURL,
		finalURL:     finalURL.String(),
		title:        title,
		source:       webFetchModeArticle,
		site:         normalizeMetadata(article.SiteName()),
		byline:       normalizeMetadata(article.Byline()),
		published:    published,
		markdown:     markdown,
	}, nil
}

func buildPageWebFetchDocument(
	doc *html.Node,
	finalURL *url.URL,
	requestedURL string,
	pageTitle string,
) (webFetchDocument, error) {
	stripWebFetchNodes(doc)

	markdown, err := convertNodeToMarkdown(doc, finalURL.String())
	if err != nil {
		return webFetchDocument{}, fmt.Errorf("convert page HTML to markdown: %w", err)
	}
	markdown = normalizeFetchedMarkdown(markdown)
	if markdown == "" {
		return webFetchDocument{}, fmt.Errorf("extracted content was empty")
	}

	title := normalizeMetadata(pageTitle)
	if title == "" {
		title = "(untitled)"
	}

	return webFetchDocument{
		requestedURL: requestedURL,
		finalURL:     finalURL.String(),
		title:        title,
		source:       webFetchModePage,
		markdown:     markdown,
	}, nil
}

func convertNodeToMarkdown(node *html.Node, domain string) (string, error) {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			tableplugin.NewTablePlugin(
				tableplugin.WithCellPaddingBehavior(tableplugin.CellPaddingBehaviorMinimal),
				tableplugin.WithHeaderPromotion(true),
				tableplugin.WithSkipEmptyRows(true),
			),
		),
	)

	output, err := conv.ConvertNode(node, converter.WithDomain(domain))
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func normalizeFetchedMarkdown(markdown string) string {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return ""
	}
	return webFetchExtraBlankLines.ReplaceAllString(markdown, "\n\n")
}

func renderWebFetchDocument(document webFetchDocument, offset int, maxChars int) (string, error) {
	contentRunes := []rune(document.markdown)
	totalChars := len(contentRunes)
	if totalChars == 0 {
		return "", fmt.Errorf("extracted content was empty")
	}
	if offset >= totalChars {
		return "", fmt.Errorf("offset %d is past end of content (%d chars)", offset, totalChars)
	}

	endOffset := offset + maxChars
	if endOffset > totalChars {
		endOffset = totalChars
	}

	more := endOffset < totalChars
	nextOffset := "none"
	if more {
		nextOffset = fmt.Sprintf("%d", endOffset)
	}

	lines := []string{
		fmt.Sprintf("URL: %s", strings.TrimSpace(document.finalURL)),
	}
	if strings.TrimSpace(document.requestedURL) != "" &&
		strings.TrimSpace(document.requestedURL) != strings.TrimSpace(document.finalURL) {
		lines = append(
			lines,
			fmt.Sprintf("Requested-URL: %s", strings.TrimSpace(document.requestedURL)),
		)
	}
	lines = append(lines,
		fmt.Sprintf("Title: %s", strings.TrimSpace(document.title)),
		fmt.Sprintf("Source: %s", strings.TrimSpace(document.source)),
	)
	if site := strings.TrimSpace(document.site); site != "" {
		lines = append(lines, fmt.Sprintf("Site: %s", site))
	}
	if byline := strings.TrimSpace(document.byline); byline != "" {
		lines = append(lines, fmt.Sprintf("Byline: %s", byline))
	}
	if published := strings.TrimSpace(document.published); published != "" {
		lines = append(lines, fmt.Sprintf("Published: %s", published))
	}
	lines = append(lines,
		fmt.Sprintf("Slice: chars %d-%d of %d", offset+1, endOffset, totalChars),
		fmt.Sprintf("More: %t", more),
		fmt.Sprintf("Next-Offset: %s", nextOffset),
		"",
		"--- CONTENT ---",
		"",
		string(contentRunes[offset:endOffset]),
	)

	return strings.Join(lines, "\n"), nil
}

func normalizeMetadata(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func extractDocumentTitle(doc *html.Node) string {
	var title string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || title != "" {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "title") {
			title = normalizeMetadata(textContent(node))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if title != "" {
				return
			}
		}
	}
	walk(doc)
	return title
}

func textContent(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}

	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(textContent(child))
		if child.NextSibling != nil {
			builder.WriteByte(' ')
		}
	}
	return builder.String()
}

func stripWebFetchNodes(node *html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if shouldStripWebFetchNode(child) {
			node.RemoveChild(child)
		} else {
			stripWebFetchNodes(child)
		}
		child = next
	}
}

func shouldStripWebFetchNode(node *html.Node) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(node.Data)) {
	case "script",
		"style",
		"noscript",
		"template",
		"svg",
		"canvas",
		"iframe",
		"form",
		"button",
		"input",
		"select",
		"textarea",
		"nav",
		"footer",
		"aside",
		"dialog",
		"picture",
		"source",
		"video",
		"audio",
		"img":
		return true
	}

	if hasAttribute(node, "hidden") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(getAttribute(node, "aria-hidden")), "true") {
		return true
	}

	style := strings.ToLower(compactWhitespace(getAttribute(node, "style")))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

func hasAttribute(node *html.Node, key string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(strings.TrimSpace(attr.Key), key) {
			return true
		}
	}
	return false
}

func getAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(strings.TrimSpace(attr.Key), key) {
			return attr.Val
		}
	}
	return ""
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), "")
}

func newWebFetchCache(ttl time.Duration, maxEntries int, now func() time.Time) *webFetchCache {
	if now == nil {
		now = time.Now
	}
	return &webFetchCache{
		now:        now,
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]webFetchCacheEntry, maxEntries),
	}
}

func (c *webFetchCache) Get(key string) (webFetchDocument, bool) {
	if c == nil {
		return webFetchDocument{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneExpiredLocked(c.now())
	entry, ok := c.entries[key]
	if !ok {
		return webFetchDocument{}, false
	}
	return entry.document, true
}

func (c *webFetchCache) Set(key string, document webFetchDocument) {
	if c == nil || c.maxEntries <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.pruneExpiredLocked(now)
	c.entries[key] = webFetchCacheEntry{
		storedAt: now,
		document: document,
	}

	for len(c.entries) > c.maxEntries {
		oldestKey := ""
		var oldestTime time.Time
		for candidateKey, entry := range c.entries {
			if oldestKey == "" || entry.storedAt.Before(oldestTime) {
				oldestKey = candidateKey
				oldestTime = entry.storedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.entries, oldestKey)
	}
}

func (c *webFetchCache) pruneExpiredLocked(now time.Time) {
	if c.ttl <= 0 {
		return
	}
	for key, entry := range c.entries {
		if now.Sub(entry.storedAt) > c.ttl {
			delete(c.entries, key)
		}
	}
}

var _ agent.Tool = (*WebFetch)(nil)
