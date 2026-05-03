// Package lightrag provides native tools for a configured LightRAG API server.
package lightrag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const (
	defaultBaseTimeout    = 180 * time.Second
	defaultQueryMode      = "hybrid"
	defaultGraphLimit     = 50
	maxErrorResponseBytes = 400
)

// Settings configures the LightRAG-backed tools.
type Settings struct {
	BaseURL             string
	APIKey              string
	WorkspaceLocalDir   string
	WorkspaceRuntimeDir string
	MemoryLocalDir      string
	MemoryRuntimeDir    string
	MediaLocalDir       string
	MediaRuntimeDir     string
	HTTPClient          *http.Client
}

type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	roots   []runtimeRoot
}

type runtimeRoot struct {
	localDir   string
	runtimeDir string
}

// NewTools constructs all LightRAG-backed tools.
func NewTools(settings Settings) ([]agent.Tool, error) {
	c, err := newClient(settings)
	if err != nil {
		return nil, err
	}
	return []agent.Tool{
		&Query{client: c},
		&Ingest{client: c},
		&Status{client: c},
		&Graph{client: c},
	}, nil
}

func newClient(settings Settings) (*client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("lightrag api url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid lightrag api url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("lightrag api url must use http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("lightrag api url must include a host")
	}

	httpClient := settings.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultBaseTimeout}
	}

	return &client{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(settings.APIKey),
		http:    httpClient,
		roots:   configuredRoots(settings),
	}, nil
}

func configuredRoots(settings Settings) []runtimeRoot {
	roots := []runtimeRoot{
		{
			localDir:   strings.TrimSpace(settings.WorkspaceLocalDir),
			runtimeDir: cleanRuntimeRoot(settings.WorkspaceRuntimeDir),
		},
		{
			localDir:   strings.TrimSpace(settings.MemoryLocalDir),
			runtimeDir: cleanRuntimeRoot(settings.MemoryRuntimeDir),
		},
		{
			localDir:   strings.TrimSpace(settings.MediaLocalDir),
			runtimeDir: cleanRuntimeRoot(settings.MediaRuntimeDir),
		},
	}
	out := make([]runtimeRoot, 0, len(roots))
	for _, root := range roots {
		if root.localDir == "" || root.runtimeDir == "" {
			continue
		}
		out = append(out, root)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].runtimeDir) == len(out[j].runtimeDir) {
			return out[i].runtimeDir < out[j].runtimeDir
		}
		return len(out[i].runtimeDir) > len(out[j].runtimeDir)
	})
	return out
}

func cleanRuntimeRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	cleaned := path.Clean(root)
	if !path.IsAbs(cleaned) {
		return ""
	}
	return cleaned
}

func (c *client) get(ctx context.Context, apiPath string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, apiPath, nil, "")
}

func (c *client) postJSON(ctx context.Context, apiPath string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request JSON: %w", err)
	}
	return c.do(ctx, http.MethodPost, apiPath, bytes.NewReader(body), "application/json")
}

func (c *client) postMultipartFile(
	ctx context.Context,
	apiPath string,
	fieldName string,
	filename string,
	file io.Reader,
) ([]byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, filename)
	if err != nil {
		return nil, fmt.Errorf("create multipart file part: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("copy multipart file content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}
	return c.do(ctx, http.MethodPost, apiPath, &body, writer.FormDataContentType())
}

func (c *client) do(
	ctx context.Context,
	method string,
	apiPath string,
	body io.Reader,
	contentType string,
) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("lightrag client is not configured")
	}
	if c.http == nil {
		return nil, fmt.Errorf("lightrag http client is not configured")
	}

	endpoint, err := c.endpoint(apiPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create lightrag request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lightrag request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read lightrag response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"lightrag API returned %s: %s",
			resp.Status,
			trimForError(string(respBody), maxErrorResponseBytes),
		)
	}
	return respBody, nil
}

func (c *client) endpoint(apiPath string) (string, error) {
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid lightrag api url: %w", err)
	}
	apiPath, rawQuery, _ := strings.Cut(apiPath, "?")
	cleanPath := "/" + strings.TrimLeft(apiPath, "/")
	parsed.Path = strings.TrimRight(parsed.Path, "/") + cleanPath
	parsed.RawQuery = rawQuery
	return parsed.String(), nil
}

func (c *client) resolveUploadPath(
	rawPath string,
) (localPath string, runtimePath string, err error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", "", fmt.Errorf("missing required argument: path")
	}
	if len(c.roots) == 0 {
		return "", "", fmt.Errorf("no upload roots are configured")
	}

	if path.IsAbs(rawPath) {
		return c.resolveAbsolutePath(path.Clean(rawPath))
	}

	workspaceRoot, ok := c.workspaceRoot()
	if !ok {
		return "", "", fmt.Errorf("workspace upload root is not configured")
	}
	cleaned := path.Clean(rawPath)
	if err := validateRelativePath(cleaned); err != nil {
		return "", "", err
	}
	return filepath.Join(workspaceRoot.localDir, filepath.FromSlash(cleaned)),
		path.Join(workspaceRoot.runtimeDir, cleaned),
		nil
}

func (c *client) resolveAbsolutePath(cleaned string) (string, string, error) {
	for _, root := range c.roots {
		if cleaned == root.runtimeDir {
			return "", "", fmt.Errorf("path must reference a file, not a root")
		}
	}
	for _, root := range c.roots {
		if strings.HasPrefix(cleaned, root.runtimeDir+"/") {
			rel := strings.TrimPrefix(cleaned, root.runtimeDir+"/")
			if err := validateRelativePath(rel); err != nil {
				return "", "", err
			}
			return filepath.Join(root.localDir, filepath.FromSlash(rel)), cleaned, nil
		}
	}

	rootNames := make([]string, 0, len(c.roots))
	for _, root := range c.roots {
		rootNames = append(rootNames, root.runtimeDir)
	}
	return "", "", fmt.Errorf(
		"absolute paths must be under %s",
		strings.Join(rootNames, ", "),
	)
}

func (c *client) workspaceRoot() (runtimeRoot, bool) {
	for _, root := range c.roots {
		if root.runtimeDir == "/workspace" {
			return root, true
		}
	}
	for _, root := range c.roots {
		if strings.Contains(root.runtimeDir, "workspace") {
			return root, true
		}
	}
	return runtimeRoot{}, false
}

func validateRelativePath(rel string) error {
	rel = path.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return fmt.Errorf("path must reference a file")
	}
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path must stay within a configured root")
	}
	return nil
}

func openRegularFile(localPath string, runtimePath string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open file %q: %w", runtimePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("stat file %q: %w", runtimePath, err)
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("path %q must reference a file", runtimePath)
	}
	return file, info, nil
}

func trimForError(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty response body)"
	}
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func prettyJSON(body []byte) string {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return strings.TrimSpace(string(body))
	}
	formatted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return strings.TrimSpace(string(body))
	}
	return string(formatted)
}
