package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist contains the Vite production build. The tracked .keep file lets Go
// tooling compile before the first frontend build.
//
//go:embed all:dist
var dist embed.FS

type Handler struct {
	assets fs.FS
}

func NewHandler() *Handler {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return &Handler{assets: assets}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requested == "." || requested == "" {
		requested = "index.html"
	}

	if info, err := fs.Stat(h.assets, requested); err == nil && !info.IsDir() {
		h.serveFile(w, r, requested)
		return
	}

	if _, err := fs.Stat(h.assets, "index.html"); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("MailManager frontend has not been built. Run npm run build --prefix web.\n"))
		return
	}

	h.serveFile(w, r, "index.html")
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	file, err := h.assets.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	reader, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "embedded asset is not seekable", http.StatusInternalServerError)
		return
	}

	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.Contains(name, "/assets/") || strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), reader)
}
