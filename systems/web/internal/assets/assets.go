// Package assets serves the embedded SPA.
package assets

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

//go:embed all:dist
var bundle embed.FS

var hashedFile = regexp.MustCompile(`[-.][A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

// Handler serves a validated bundle.
type Handler struct {
	files fs.FS
	root  *os.Root
}

// Load selects the embedded bundle or a confined development directory.
func Load(directory string) (*Handler, error) {
	if directory != "" {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return nil, fmt.Errorf("Q15_WEB_DIR: %w", err)
		}
		handler, err := New(root.FS())
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		handler.root = root
		return handler, nil
	}
	files, err := fs.Sub(bundle, "dist")
	if err != nil {
		return nil, err
	}
	return New(files)
}

// New rejects a missing or incomplete build rather than serving a placeholder.
func New(files fs.FS) (*Handler, error) {
	info, err := fs.Stat(files, "index.html")
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("web bundle must contain a nonempty dist/index.html")
	}
	return &Handler{files: files}, nil
}

// Close releases the confined development filesystem.
func (h *Handler) Close() error {
	if h.root != nil {
		return h.root.Close()
	}
	return nil
}

// ServeHTTP serves files, with SPA fallback only for non-API routes in a real bundle.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "api" || strings.HasPrefix(name, "api/") || name == "ws" ||
		strings.HasPrefix(name, "ws/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("{\"error\":\"not_found\"}\n"))
		return
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			http.NotFound(w, r)
			return
		}
	}
	if name == "" {
		name = "index.html"
	}
	info, err := fs.Stat(h.files, name)
	if err != nil || !info.Mode().IsRegular() {
		if name == "assets" || strings.HasPrefix(name, "assets/") || name == "sw.js" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	w.Header().Set("Cache-Control", "no-cache")
	if name != "index.html" && name != "sw.js" && hashedFile.MatchString(path.Base(name)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	data, err := fs.ReadFile(h.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err = fs.Stat(h.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(data))
}
