package web

import (
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// maxExcerptRunes is the hard cap on Result.Snippet. It is a package constant
// rather than a helper's local default so that the invariant is visible in the
// Result and Provider doc comments: a third adapter that returns a page body
// fails review against them instead of shipping 80 kB snippets.
const maxExcerptRunes = 1200

var (
	// tagPattern matches one HTML tag. The bounded repetition keeps a stray
	// "<" in prose from scanning the rest of a multi-megabyte document (the
	// repetition bound is 1000 because that is Go regexp's maximum).
	tagPattern = regexp.MustCompile(`(?s)<[^<>]{0,1000}>`)
	// spacePattern matches any run of whitespace, including the non-breaking
	// spaces that come out of entity unescaping.
	spacePattern = regexp.MustCompile(`[\s\x{00a0}]+`)
)

// excerptStopWords are dropped from query terms before they are used to score
// excerpt windows: they appear in nearly every document and would drown the
// signal from the words that actually select one.
var excerptStopWords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "that": {},
	"this": {}, "these": {}, "those": {}, "you": {}, "your": {}, "are": {},
	"was": {}, "were": {}, "how": {}, "what": {}, "when": {}, "where": {},
	"why": {}, "use": {}, "using": {}, "into": {}, "about": {}, "any": {},
	"can": {}, "does": {}, "did": {}, "not": {}, "but": {}, "his": {},
	"her": {}, "its": {}, "our": {}, "their": {}, "than": {}, "then": {},
	"there": {}, "here": {}, "will": {}, "would": {}, "should": {}, "could": {},
}

// cleanText turns provider text into the single-line form the renderer
// assumes: HTML entities unescaped, tags stripped, whitespace runs (newlines
// and hard-wrapped lines included) collapsed to single spaces.
//
// Tags are stripped before entities are unescaped on purpose. Unescaping
// first would turn "a &lt; b &gt; c" into what looks like a tag and delete the
// prose between the comparisons.
func cleanText(text string) string {
	if text == "" {
		return ""
	}
	text = tagPattern.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = spacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// queryTerms splits a search query into the lowercased terms used to score
// excerpt windows, dropping stop words and one-rune fragments.
func queryTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"'()[]{}<>:;,.!?/\\|-_*`)
		if len([]rune(field)) < 2 {
			continue
		}
		if _, stop := excerptStopWords[field]; stop {
			continue
		}
		terms = append(terms, field)
	}
	if len(terms) == 0 {
		// A query made entirely of stop words still has to score somehow.
		for _, field := range fields {
			if field != "" {
				terms = append(terms, field)
			}
		}
	}
	return terms
}

// excerpt returns at most max runes of text (default maxExcerptRunes), always
// normalised through cleanText first.
//
// The window is chosen by scanning the whole normalised text for the query
// terms and returning the max-rune window with the most hits, so a passage
// buried at rune 30 000 is still found and a nav/TOC block at the front is not
// returned just because it comes first. Leading text is only returned when no
// query term occurs anywhere in the document, in which case no window is
// better informed than the first one. An ellipsis marks each cut edge.
func excerpt(text string, terms []string, max int) string {
	if max <= 0 {
		max = maxExcerptRunes
	}
	text = cleanText(text)
	if text == "" {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= max {
		return text
	}

	// Reserve room for the ellipses inside the budget so the return value
	// never exceeds max runes.
	size := max - 2
	if size < 1 {
		size = max
	}

	start, end := bestWindow(runes, terms, size)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// bestWindow returns the [start, end) rune range of at most size runes with
// the most query-term hits.
func bestWindow(runes []rune, terms []string, size int) (int, int) {
	if size >= len(runes) {
		return 0, len(runes)
	}

	positions := termPositions(runes, terms)
	if len(positions) == 0 {
		return snapWindow(runes, 0, size)
	}
	sort.Ints(positions)

	bestStart := 0
	bestScore := 0
	seen := make(map[int]struct{}, len(positions))
	for _, pos := range positions {
		start := pos - size/4
		if start < 0 {
			start = 0
		}
		if start+size > len(runes) {
			start = len(runes) - size
		}
		if _, ok := seen[start]; ok {
			continue
		}
		seen[start] = struct{}{}
		if score := countInRange(positions, start, start+size); score > bestScore {
			bestStart, bestScore = start, score
		}
	}
	return snapWindow(runes, bestStart, size)
}

// termPositions returns the rune offset of every query-term occurrence.
func termPositions(runes []rune, terms []string) []int {
	lower := []rune(strings.ToLower(string(runes)))
	positions := make([]int, 0, 32)
	for _, term := range terms {
		tr := []rune(strings.ToLower(strings.TrimSpace(term)))
		if len(tr) == 0 {
			continue
		}
		for i := 0; i+len(tr) <= len(lower); i++ {
			if runesMatchAt(lower, i, tr) {
				positions = append(positions, i)
			}
		}
	}
	return positions
}

func runesMatchAt(haystack []rune, at int, needle []rune) bool {
	for i, r := range needle {
		if haystack[at+i] != r {
			return false
		}
	}
	return true
}

// countInRange counts the sorted positions inside [lo, hi).
func countInRange(sorted []int, lo, hi int) int {
	return sort.SearchInts(sorted, hi) - sort.SearchInts(sorted, lo)
}

// snapWindow moves a window onto word boundaries where it can, so an excerpt
// does not begin or end mid-word.
func snapWindow(runes []rune, start, size int) (int, int) {
	end := start + size
	if end > len(runes) {
		end = len(runes)
	}
	if start > 0 {
		for i := start; i < end && i < start+32; i++ {
			if unicode.IsSpace(runes[i]) {
				start = i + 1
				break
			}
		}
	}
	if end < len(runes) {
		for i := end; i > start && i > end-32; i-- {
			if unicode.IsSpace(runes[i-1]) {
				end = i - 1
				break
			}
		}
	}
	if end <= start {
		end = start + 1
		if end > len(runes) {
			end = len(runes)
		}
	}
	return start, end
}

// titleKey reduces a title to its lowercased letters and digits, so that
// markup, punctuation and spacing differences do not defeat a comparison.
func titleKey(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// titleCoreKey is titleKey with a trailing " - Site" / " | Site" suffix
// dropped, so the same article on two syndicating domains compares equal.
func titleCoreKey(title string) string {
	core := ""
	for _, sep := range []string{" | ", " - ", " — ", " · ", " :: "} {
		i := strings.LastIndex(title, sep)
		if i <= 0 {
			continue
		}
		head := strings.TrimSpace(title[:i])
		if len([]rune(head)) >= 12 && len([]rune(head)) > len([]rune(core)) {
			core = head
		}
	}
	if core == "" {
		return titleKey(title)
	}
	return titleKey(core)
}

// sameTitle reports whether two titles are the same result, either verbatim
// after normalisation or once a site-name suffix is dropped.
func sameTitle(a, b string) bool {
	keyA, keyB := titleKey(a), titleKey(b)
	if keyA != "" && keyA == keyB {
		return true
	}
	coreA, coreB := titleCoreKey(a), titleCoreKey(b)
	return coreA != "" && coreA == coreB
}

// stripTitleEcho drops a leading run of body text that repeats the result
// title, which is what a page rendered into a provider's content field looks
// like: the H1 arrives as the first body line, directly under a title that
// already said it.
func stripTitleEcho(title, body string) string {
	key := titleKey(title)
	if key == "" || body == "" {
		return body
	}

	head := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		head = body[:i]
	}
	headRunes := []rune(head)
	if limit := len([]rune(key)) + 12; len(headRunes) > limit {
		head = string(headRunes[:limit])
	}
	if !strings.HasPrefix(titleKey(head), key) {
		return body
	}

	bodyRunes := []rune(body)
	matched := 0
	cut := 0
	lastMatched := 0
	for i, r := range bodyRunes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if matched < len([]rune(key)) {
				matched++
				lastMatched = i + 1
			}
			continue
		}
		if matched >= len([]rune(key)) {
			cut = i + 1
			break
		}
	}
	if cut == 0 {
		if matched < len([]rune(key)) {
			return body
		}
		cut = lastMatched
	}
	if cut >= len(bodyRunes) {
		return body
	}
	return strings.TrimSpace(string(bodyRunes[cut:]))
}

// canonicalURL normalises a result URL for deduplication and for rendering:
// scheme and host lowercased, default ports and fragments dropped, known
// tracking parameters stripped, trailing slashes removed and the query string
// sorted.
//
// Only known tracking keys are removed. Over-eager stripping can delete a
// parameter that selects the document, which is why ?id= and ?v= survive:
// they are far more often the page identity than a campaign tag.
func canonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return raw
	}

	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" && port != defaultPort(scheme) {
		host = host + ":" + port
	}

	query := parsed.Query()
	for key := range query {
		if isTrackingParam(key) {
			query.Del(key)
		}
	}

	path := parsed.Path
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	if path == "/" {
		path = ""
	}

	canonical := url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     path,
		RawQuery: query.Encode(),
	}
	return canonical.String()
}

// isTrackingParam reports whether a query key is a known campaign or
// click-attribution tag rather than part of the document identity.
func isTrackingParam(key string) bool {
	key = strings.ToLower(key)
	if strings.HasPrefix(key, "utm_") {
		return true
	}
	switch key {
	case "fbclid", "gclid", "dclid", "msclkid", "yclid", "igshid",
		"mc_cid", "mc_eid", "ref", "ref_src", "ref_url":
		return true
	}
	return false
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// documentExtensions is the set of suffixes that mark a result as a document
// rather than a page. Documents stay in the result set: the extension is the
// only reliable signal and dropping them would hide legitimate sources, so
// they are marked instead.
var documentExtensions = []string{".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".rtf", ".epub"}

// isDocumentURL reports whether a URL points at a document.
func isDocumentURL(raw string) bool {
	lower := strings.ToLower(raw)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	for _, ext := range documentExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// truncateSnippet cuts a snippet to at most max runes at a word boundary,
// marking the cut with an ellipsis.
func truncateSnippet(snippet string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(snippet)
	if len(runes) <= max {
		return snippet
	}
	cut := max - 1
	if cut < 1 {
		// max is 1: an ellipsis alone is the only value that fits the cap.
		return "…"
	}
	for i := cut; i > cut/2 && i > 0; i-- {
		if unicode.IsSpace(runes[i-1]) {
			cut = i - 1
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}
