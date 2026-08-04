// Package webui serves the Angular application embedded in production builds.
package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

var fingerprintedAsset = regexp.MustCompile(`-[A-Za-z0-9]{8,}\.[A-Za-z0-9]+$`)

type spaHandler struct {
	assets fs.FS
	files  http.Handler
	index  []byte
}

func newSPAHandler(assets fs.FS) http.Handler {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic(err)
	}
	return &spaHandler{
		assets: assets,
		files:  http.FileServer(http.FS(assets)),
		index:  index,
	}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" || name == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if info, err := fs.Stat(h.assets, name); err == nil && !info.IsDir() {
		setCachePolicy(w, name)
		h.files.ServeHTTP(w, r)
		return
	}
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func setCachePolicy(w http.ResponseWriter, name string) {
	if fingerprintedAsset.MatchString(path.Base(name)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
}
