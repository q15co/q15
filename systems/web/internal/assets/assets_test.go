package assets

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundleAssertion(t *testing.T) {
	for _, test := range []struct {
		name  string
		files fstest.MapFS
		valid bool
	}{
		{"missing", fstest.MapFS{}, false},
		{"placeholder", fstest.MapFS{".gitkeep": {}}, false},
		{"partial build", fstest.MapFS{".gitkeep": {}, "assets/app-12345678.js": {Data: []byte("app")}}, false},
		{"empty index", fstest.MapFS{"index.html": {}}, false},
		{"built", fstest.MapFS{"index.html": {Data: []byte("shell")}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.files); (err == nil) != test.valid {
				t.Fatalf("New = %v, valid %v", err, test.valid)
			}
		})
	}
}

func TestSPAFallbackAndCache(t *testing.T) {
	h, err := New(
		fstest.MapFS{
			"index.html":             {Data: []byte("shell")},
			"assets/app-aB123456.js": {Data: []byte("app")},
			"sw.js":                  {Data: []byte("worker")},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path        string
		want        int
		body, cache string
	}{
		{"/", 200, "shell", "no-cache"}, {"/index.html", 200, "shell", "no-cache"}, {"/deep/link", 200, "shell", "no-cache"},
		{"/assets/app-aB123456.js", 200, "app", "public, max-age=31536000, immutable"}, {"/sw.js", 200, "worker", "no-cache"},
		{"/assets/missing.js", 404, "", ""}, {"/api/nope", 404, "not_found", ""}, {"/api", 404, "not_found", ""}, {"/ws/nope", 404, "not_found", ""},
		{"/.env", 404, "", ""}, {"/assets", 404, "", ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
		if w.Code != test.want || !strings.Contains(w.Body.String(), test.body) {
			t.Errorf("%s = %d/%s", test.path, w.Code, w.Body)
		}
		if test.cache != "" && w.Header().Get("Cache-Control") != test.cache {
			t.Errorf("%s cache %q", test.path, w.Header().Get("Cache-Control"))
		}
	}
}

func TestDirectoryOverrideSmoke(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>development</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/deep/link", nil))
	if w.Code != 200 || w.Body.String() != "<html>development</html>" {
		t.Fatalf("directory override = %d/%s", w.Code, w.Body)
	}
	// The override cannot serve a symlink reaching outside its selected root.
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/leak", nil))
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("override escaped root")
	}
}
