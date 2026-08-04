//go:build !embed_frontend

package webui

import "net/http"

func Handler() (http.Handler, bool) {
	return nil, false
}
