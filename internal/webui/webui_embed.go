//go:build embed_frontend

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist/browser
var assets embed.FS

func Handler() (http.Handler, bool) {
	root, err := fs.Sub(assets, "dist/browser")
	if err != nil {
		panic(err)
	}
	return newSPAHandler(root), true
}
