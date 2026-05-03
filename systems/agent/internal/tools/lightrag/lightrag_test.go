package lightrag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestNewToolsDefinitions(t *testing.T) {
	tools := newTestTools(t, "http://lightrag:9621", nil)

	if got, want := toolNames(tools), []string{
		"rag_query",
		"rag_ingest",
		"rag_status",
		"rag_graph",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestToolsRejectInvalidJSON(t *testing.T) {
	tools := newTestTools(t, "http://lightrag:9621", nil)

	for _, name := range []string{"rag_query", "rag_ingest", "rag_status", "rag_graph"} {
		t.Run(name, func(t *testing.T) {
			_, err := tools[name].Run(context.Background(), "{")
			if err == nil || !strings.Contains(err.Error(), "invalid arguments JSON") {
				t.Fatalf("Run() error = %v, want invalid JSON error", err)
			}
		})
	}
}

func TestQueryPostsSynthesizedRequestWithAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAPIKey(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/query" {
			t.Fatalf("request = %s %s, want POST /query", r.Method, r.URL.Path)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["query"] != "What does Mattei say?" {
			t.Fatalf("query = %#v, want question", payload["query"])
		}
		if payload["mode"] != "hybrid" {
			t.Fatalf("mode = %#v, want hybrid default", payload["mode"])
		}
		if payload["only_need_context"] != false {
			t.Fatalf("only_need_context = %#v, want false", payload["only_need_context"])
		}
		if payload["include_references"] != true {
			t.Fatalf("include_references = %#v, want true", payload["include_references"])
		}
		if payload["enable_rerank"] != false {
			t.Fatalf("enable_rerank = %#v, want false default", payload["enable_rerank"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"response": "Mattei discusses participation.",
			"references": [
				{"reference_id":"1","file_path":"/workspace/library-markdown/book.md"}
			]
		}`))
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	got, err := tools["rag_query"].Run(
		context.Background(),
		`{"query":"What does Mattei say?"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Response:",
		"Mattei discusses participation.",
		"References:",
		"[1] /workspace/library-markdown/book.md",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestQueryCanEnableRerank(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["enable_rerank"] != true {
			t.Fatalf("enable_rerank = %#v, want true override", payload["enable_rerank"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok"}`))
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	if _, err := tools["rag_query"].Run(
		context.Background(),
		`{"query":"What does Mattei say?","enable_rerank":true}`,
	); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestQueryRejectsUnsupportedMode(t *testing.T) {
	tools := newTestTools(t, "http://lightrag:9621", nil)

	_, err := tools["rag_query"].Run(context.Background(), `{"query":"hello","mode":"bypass"}`)
	if err == nil || !strings.Contains(err.Error(), "mode must be one of") {
		t.Fatalf("Run() error = %v, want unsupported mode error", err)
	}
}

func TestIngestUploadsMultipartFile(t *testing.T) {
	roots := newTestRoots(t)
	writeTestFile(t, filepath.Join(roots.workspace, "docs", "book.md"), "book text")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAPIKey(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/documents/upload" {
			t.Fatalf("request = %s %s, want POST /documents/upload", r.Method, r.URL.Path)
		}

		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader() error = %v", err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}
		if part.FormName() != "file" {
			t.Fatalf("form field = %q, want file", part.FormName())
		}
		if part.FileName() != "book.md" {
			t.Fatalf("filename = %q, want book.md", part.FileName())
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart file: %v", err)
		}
		if string(data) != "book text" {
			t.Fatalf("multipart file = %q, want book text", data)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"message":"queued",
			"track_id":"upload-123"
		}`))
	}))
	defer server.Close()

	tools := newTestToolsWithRoots(t, server.URL, server.Client(), roots)
	got, err := tools["rag_ingest"].Run(context.Background(), `{"path":"docs/book.md"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Source: /workspace/docs/book.md",
		"Status: success",
		"Track-ID: upload-123",
		"Message: queued",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestIngestInsertsTextWithSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAPIKey(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/documents/text" {
			t.Fatalf("request = %s %s, want POST /documents/text", r.Method, r.URL.Path)
		}

		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["text"] != "generated note" {
			t.Fatalf("text = %q, want generated note", payload["text"])
		}
		if payload["file_source"] != "note:manual" {
			t.Fatalf("file_source = %q, want note:manual", payload["file_source"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"message":"queued",
			"track_id":"insert-123"
		}`))
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	got, err := tools["rag_ingest"].Run(
		context.Background(),
		`{"text":"generated note","source":"note:manual"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(got, "Track-ID: insert-123") {
		t.Fatalf("output = %q, want track id", got)
	}
}

func TestIngestRejectsPathsOutsideConfiguredRoots(t *testing.T) {
	tools := newTestTools(t, "http://lightrag:9621", nil)

	_, err := tools["rag_ingest"].Run(context.Background(), `{"path":"/skills/demo.md"}`)
	if err == nil || !strings.Contains(err.Error(), "absolute paths must be under") {
		t.Fatalf("Run() error = %v, want root restriction error", err)
	}
}

func TestStatusChecksTrackAndPipelineEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAPIKey(t, r)
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/documents/track_status/upload-123":
			_, _ = w.Write([]byte(`{"track_id":"upload-123","total_count":1}`))
		case "/documents/pipeline_status":
			_, _ = w.Write([]byte(`{"busy":false,"job_name":"Default Job"}`))
		case "/documents/status_counts":
			_, _ = w.Write([]byte(`{"status_counts":{"PROCESSED":2}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	trackOut, err := tools["rag_status"].Run(
		context.Background(),
		`{"track_id":"upload-123"}`,
	)
	if err != nil {
		t.Fatalf("track Run() error = %v", err)
	}
	if !strings.Contains(trackOut, "Track Status:") ||
		!strings.Contains(trackOut, `"track_id": "upload-123"`) {
		t.Fatalf("track output = %q, want track status", trackOut)
	}

	pipelineOut, err := tools["rag_status"].Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("pipeline Run() error = %v", err)
	}
	if !strings.Contains(pipelineOut, "Pipeline Status:") ||
		!strings.Contains(pipelineOut, "Document Status Counts:") {
		t.Fatalf("pipeline output = %q, want combined status", pipelineOut)
	}

	wantPaths := []string{
		"/documents/track_status/upload-123",
		"/documents/pipeline_status",
		"/documents/status_counts",
	}
	if !equalStrings(paths, wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
}

func TestGraphSearchesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAPIKey(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/graph/label/search" {
			t.Fatalf("request = %s %s, want GET /graph/label/search", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "budget" {
			t.Fatalf("q = %q, want budget", got)
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit = %q, want 50", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["Participatory Budgeting","Municipal Budget"]`))
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	got, err := tools["rag_graph"].Run(context.Background(), `{"query":"budget"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		`Graph labels matching "budget":`,
		"- Participatory Budgeting",
		"- Municipal Budget",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestHTTPErrorIncludesStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad rag request", http.StatusBadGateway)
	}))
	defer server.Close()

	tools := newTestTools(t, server.URL, server.Client())
	_, err := tools["rag_query"].Run(context.Background(), `{"query":"hello"}`)
	if err == nil ||
		!strings.Contains(err.Error(), "502") ||
		!strings.Contains(err.Error(), "bad rag request") {
		t.Fatalf("Run() error = %v, want status and body", err)
	}
}

type testRoots struct {
	workspace string
	memory    string
	media     string
}

func newTestTools(
	t *testing.T,
	baseURL string,
	httpClient *http.Client,
) map[string]agent.Tool {
	t.Helper()
	return newTestToolsWithRoots(t, baseURL, httpClient, newTestRoots(t))
}

func newTestToolsWithRoots(
	t *testing.T,
	baseURL string,
	httpClient *http.Client,
	roots testRoots,
) map[string]agent.Tool {
	t.Helper()
	tools, err := NewTools(Settings{
		BaseURL:             baseURL,
		APIKey:              "test-key",
		WorkspaceLocalDir:   roots.workspace,
		WorkspaceRuntimeDir: "/workspace",
		MemoryLocalDir:      roots.memory,
		MemoryRuntimeDir:    "/memory",
		MediaLocalDir:       roots.media,
		MediaRuntimeDir:     "/media",
		HTTPClient:          httpClient,
	})
	if err != nil {
		t.Fatalf("NewTools() error = %v", err)
	}

	out := make(map[string]agent.Tool, len(tools))
	for _, tool := range tools {
		out[tool.Definition().Name] = tool
	}
	return out
}

func newTestRoots(t *testing.T) testRoots {
	t.Helper()
	root := t.TempDir()
	roots := testRoots{
		workspace: filepath.Join(root, "workspace"),
		memory:    filepath.Join(root, "memory"),
		media:     filepath.Join(root, "media"),
	}
	for _, dir := range []string{roots.workspace, roots.memory, roots.media} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	return roots
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir test file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func assertAPIKey(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("X-API-Key"); got != "test-key" {
		t.Fatalf("X-API-Key = %q, want test-key", got)
	}
}

func toolNames(tools map[string]agent.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, name := range []string{"rag_query", "rag_ingest", "rag_status", "rag_graph"} {
		if _, ok := tools[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

func equalStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
