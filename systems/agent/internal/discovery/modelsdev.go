package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
	"golang.org/x/net/publicsuffix"
)

const (
	// modelsDevAPIURL is the default source for the models.dev catalog.
	modelsDevAPIURL = "https://models.dev/api.json"
	// modelsDevCacheFileName is the cache file name within the cache directory.
	modelsDevCacheFileName = "models-dev.json"
	// modelsDevCacheSubDir is the default cache directory under the OS temp
	// dir when Q15_DISCOVERY_CACHE_DIR is unset.
	modelsDevCacheSubDir = "q15-discovery"
	// modelsDevDefaultTTL is the cache freshness window.
	modelsDevDefaultTTL = 24 * time.Hour
)

const (
	envCacheDir = "Q15_DISCOVERY_CACHE_DIR"
	envCacheTTL = "Q15_DISCOVERY_CACHE_TTL"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// modelsDevClient fetches and caches the models.dev api.json catalog, then
// enriches roster entries with capabilities, context limits, modalities, and
// cost. It is safe to construct offline: no network call happens until Enrich
// is first invoked.
//
// models.dev publishes NO benchmark data — BenchmarkScores is always left nil
// from this source (the field remains on Model for potential future providers).
type modelsDevClient struct {
	http      httpDoer
	sourceURL string

	mu       sync.Mutex
	cached   modelsDevFile
	cachedAt time.Time
}

func newModelsDevClient(httpClient *http.Client) *modelsDevClient {
	return newModelsDevClientWithSource(httpClient, modelsDevAPIURL)
}

func newModelsDevClientWithSource(httpClient *http.Client, sourceURL string) *modelsDevClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = modelsDevAPIURL
	}
	return &modelsDevClient{
		http:      httpClient,
		sourceURL: sourceURL,
	}
}

// modelsDevFile maps provider slug → provider catalog.
type modelsDevFile map[string]modelsDevProvider

type modelsDevProvider struct {
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	Doc    string                    `json:"doc"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevEntry struct {
	modelID string
	model   modelsDevModel
}

type modelsDevModel struct {
	Name             string              `json:"name"`
	ReleaseDate      string              `json:"release_date"`
	Limit            modelsDevLimit      `json:"limit"`
	Modalities       modelsDevModalities `json:"modalities"`
	Cost             modelsDevCost       `json:"cost"`
	ToolCall         bool                `json:"tool_call"`
	Reasoning        bool                `json:"reasoning"`
	Attachment       bool                `json:"attachment"`
	StructuredOutput bool                `json:"structured_output"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// modelsDevCost holds the per-million-token USD prices as published by
// models.dev. The values are already USD per million tokens (NOT per-token) —
// confirmed from the models.dev source at https://github.com/sst/models.dev
// whose README states: "cost.input: Cost per million input tokens (USD)".
// Many models (notably all ollama-cloud entries as of 2026-06-21) have no cost
// block at all, in which case both fields are zero (unknown).
type modelsDevCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// models.dev does not publish benchmark data. There is no benchmarks field
// anywhere in the live api.json catalog (verified 2026-06-21: 0 of ~thousands
// of models have one). BenchmarkScores is therefore always left nil from this
// source.

// Enrich returns models.dev-derived entries aligned to the base roster. The
// caller Merge-joins them onto the roster by ProviderModel. When the catalog
// cannot be loaded (offline, fetch failure, parse error) Enrich returns nil
// and no error — enrichment is best-effort and non-fatal.
func (c *modelsDevClient) Enrich(ctx context.Context, p Provider, base []Model) ([]Model, error) {
	if len(base) == 0 {
		return nil, nil
	}
	file, err := c.loadFile(ctx)
	if err != nil || len(file) == 0 {
		return nil, nil
	}
	slug := providerSlug(p, file)
	providerData, ok := file[slug]
	if !ok || len(providerData.Models) == 0 {
		return nil, nil
	}
	byKey := make(map[string]modelsDevEntry, len(providerData.Models))
	for id, md := range providerData.Models {
		key := strings.ToLower(strings.TrimSpace(id))
		if _, exists := byKey[key]; exists {
			// Case-folding must not choose arbitrarily between distinct IDs.
			byKey[key] = modelsDevEntry{}
			continue
		}
		byKey[key] = modelsDevEntry{modelID: id, model: md}
	}

	enriched := make([]Model, 0, len(base))
	for _, b := range base {
		var matchedID string
		var matchedModel modelsDevModel
		for _, key := range modelcatalog.CandidateKeys(b.ProviderModel) {
			if entry, ok := byKey[key]; ok && entry.modelID != "" {
				matchedID = entry.modelID
				matchedModel = entry.model
				break
			}
		}
		if matchedID == "" {
			continue
		}
		enriched = append(enriched, modelFromModelsDev(matchedID, matchedModel))
	}
	return enriched, nil
}

// loadFile returns the parsed models.dev catalog. It prefers a fresh on-disk
// cache; on cache miss or expiry it fetches from the network; on fetch failure
// it falls back to a stale cache. A successfully loaded catalog is memoized
// in-memory for the TTL window.
func (c *modelsDevClient) loadFile(ctx context.Context) (modelsDevFile, error) {
	ttl := cacheTTL()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached != nil && ttl > 0 && time.Since(c.cachedAt) < ttl {
		return c.cached, nil
	}

	cachePath := cacheFilePath()
	fresh, fileData := readCache(cachePath, ttl)

	if fresh && len(fileData) > 0 {
		if file, err := parseModelsDevFile(fileData); err == nil {
			c.cached = file
			c.cachedAt = time.Now()
			return file, nil
		}
	}

	data, err := c.fetch(ctx)
	if err != nil {
		// Fallback to stale cache when the network fetch failed.
		if len(fileData) > 0 {
			if file, perr := parseModelsDevFile(fileData); perr == nil {
				c.cached = file
				c.cachedAt = time.Now()
				return file, nil
			}
		}
		return nil, err
	}

	if err := writeCache(cachePath, data); err != nil {
		// Cache write failure is non-fatal.
		_ = err
	}

	file, err := parseModelsDevFile(data)
	if err != nil {
		return nil, fmt.Errorf("parse models.dev response: %w", err)
	}
	c.cached = file
	c.cachedAt = time.Now()
	return file, nil
}

func (c *modelsDevClient) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("models.dev returned an empty response")
	}
	return data, nil
}

func parseModelsDevFile(data []byte) (modelsDevFile, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var file modelsDevFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return file, nil
}

// modelFromModelsDev converts one models.dev entry into a discovery Model.
//
// Capability mapping uses the verified live schema (2026-06-21):
//   - tool_call (top-level bool) → ToolCalling
//   - reasoning (top-level bool) → Reasoning
//   - attachment (top-level bool) OR modalities.input contains "image" → ImageInput
//   - modalities.input contains "audio" → AudioInput
//   - modalities.input contains "video" → VideoInput
//   - modalities.input contains "text" → Text (defaulted to true when absent)
//
// release_date (ISO date string) → ReleaseDate. structured_output (top-level
// bool) → StructuredOutput. Cost comes from the cost block (already USD per
// million tokens). models.dev publishes no benchmarks.
func modelFromModelsDev(id string, md modelsDevModel) Model {
	caps := Capabilities{Known: true}
	for _, modality := range md.Modalities.Input {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "text":
			caps.Text = true
		case "image":
			caps.ImageInput = true
		case "audio":
			caps.AudioInput = true
		}
	}
	// models.dev exposes capabilities as top-level booleans (NOT a nested
	// capability block).
	if md.ToolCall {
		caps.ToolCalling = true
	}
	if md.Reasoning {
		caps.Reasoning = true
	}
	// "attachment" is the models.dev vision/multimodal attachment flag. We
	// OR it with the modalities-derived signal for robustness.
	if md.Attachment {
		caps.ImageInput = true
	}
	// A model with no recorded input modality still accepts text by default.
	if !caps.Text && !caps.ImageInput && !caps.AudioInput {
		caps.Text = true
	}

	// models.dev cost is already USD per million tokens (confirmed from the
	// source docs). No per-token → per-MTok conversion is needed.
	costIn := md.Cost.Input
	costOut := md.Cost.Output

	name := strings.TrimSpace(md.Name)
	if name == "" {
		name = id
	}

	var releaseDate time.Time
	if rd := strings.TrimSpace(md.ReleaseDate); rd != "" {
		if parsed, err := time.Parse("2006-01-02", rd); err == nil {
			releaseDate = parsed
		}
	}

	videoInput := false
	for _, modality := range md.Modalities.Input {
		if strings.EqualFold(strings.TrimSpace(modality), "video") {
			videoInput = true
			break
		}
	}

	return Model{
		ProviderModel:         id,
		Name:                  name,
		Capabilities:          caps,
		CostTier:              deriveCostTier(costIn, costOut),
		CostPerMTokIn:         costIn,
		CostPerMTokOut:        costOut,
		MaxContextTokens:      md.Limit.Context,
		MaxOutputTokens:       md.Limit.Output,
		ReleaseDate:           releaseDate,
		VideoInput:            videoInput,
		StructuredOutput:      md.StructuredOutput,
		StructuredOutputKnown: true,
		Source:                sourceModelsDev,
	}
}

// deriveCostTier classifies a model by combined per-million-token cost. It
// returns "" when pricing is unknown.
func deriveCostTier(costInPerMTok, costOutPerMTok float64) string {
	total := costInPerMTok + costOutPerMTok
	if total <= 0 {
		return ""
	}
	switch {
	case total < 1:
		return "cheap"
	case total < 5:
		return "standard"
	default:
		return "expensive"
	}
}

// providerSlug resolves catalog metadata without treating the operator's
// provider name as an identity. Explicit aliases support proxies; otherwise
// endpoint matches take precedence over a unique documented domain.
func providerSlug(p Provider, catalog modelsDevFile) string {
	if slug := strings.TrimSpace(p.Options.ModelsDevProvider); slug != "" {
		return slug
	}
	switch providertypes.NormalizeOrEmpty(p.Type) {
	case providertypes.OpenAICodex:
		return "openai"
	case providertypes.Ollama, providertypes.OpenAICompatible:
		return catalogProviderSlug(p.BaseURL, catalog)
	}
	return ""
}

func catalogProviderSlug(baseURL string, catalog modelsDevFile) string {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Hostname() == "" {
		return ""
	}
	matchedSlug, bestScore, ambiguous := "", 0, false
	for slug, provider := range catalog {
		score := catalogEndpointScore(base, provider.API)
		if score == 0 {
			continue
		}
		if score > bestScore {
			matchedSlug, bestScore, ambiguous = slug, score, false
		} else if score == bestScore {
			ambiguous = true
		}
	}
	if bestScore > 0 {
		if ambiguous {
			return ""
		}
		return matchedSlug
	}
	domain := registeredDomain(baseURL)
	if domain == "" {
		return ""
	}
	for slug, provider := range catalog {
		if registeredDomain(provider.Doc) != domain && registeredDomain(provider.API) != domain {
			continue
		}
		if matchedSlug != "" {
			return ""
		}
		matchedSlug = slug
	}
	return matchedSlug
}

func catalogEndpointScore(base *url.URL, raw string) int {
	api, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || api.Host == "" || !strings.EqualFold(api.Host, base.Host) {
		return 0
	}
	apiPath := strings.TrimRight(api.Path, "/")
	basePath := strings.TrimRight(base.Path, "/")
	if basePath == apiPath || strings.HasPrefix(basePath, apiPath+"/") {
		return len(apiPath) + 2
	}
	return 1
}

func registeredDomain(raw string) string {
	host := baseURLHost(raw)
	if net.ParseIP(host) != nil {
		return ""
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return domain
}

// baseURLHost extracts the hostname from a base URL string. Returns "" on
// parse failure or empty input.
func baseURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		return strings.ToLower(strings.TrimSpace(u.Hostname()))
	}
	return ""
}

// --- cache helpers ---

func cacheFilePath() string {
	return filepath.Join(cacheDir(), modelsDevCacheFileName)
}

func cacheDir() string {
	if v := strings.TrimSpace(os.Getenv(envCacheDir)); v != "" {
		return v
	}
	return filepath.Join(os.TempDir(), modelsDevCacheSubDir)
}

func cacheTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv(envCacheTTL)); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return modelsDevDefaultTTL
}

// readCache reads the cache file and reports whether it is fresh relative to
// ttl. A missing or unreadable cache returns (false, nil). A stale cache
// returns (false, data) so the caller can reuse it as a fallback.
func readCache(path string, ttl time.Duration) (fresh bool, data []byte) {
	info, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	if ttl > 0 && time.Since(info.ModTime()) > ttl {
		return false, data
	}
	return true, data
}

// writeCache atomically writes data to path (temp file + rename).
func writeCache(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".models-dev-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
