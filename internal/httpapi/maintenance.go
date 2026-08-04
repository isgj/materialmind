package httpapi

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"time"

	"materialmind/internal/store"
)

func (a *API) getStorageSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.GetStorageSettings(r.Context())
	writeResult(w, settings, err)
}

func (a *API) updateStorageSettings(w http.ResponseWriter, r *http.Request) {
	var request store.StorageSettings
	if !decodeJSON(w, r, &request) {
		return
	}
	settings, err := a.store.UpdateStorageSettings(r.Context(), request)
	if err == nil && a.engine != nil {
		_, err = a.engine.ApplyRetention(r.Context())
	}
	writeResult(w, settings, err)
}

func (a *API) downloadBackup(w http.ResponseWriter, r *http.Request) {
	temporary, err := os.CreateTemp("", "materialmind-backup-*.db")
	if err != nil {
		writeAPIError(w, fmt.Errorf("create temporary backup path: %w", err))
		return
	}
	path := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(path)
		writeAPIError(w, fmt.Errorf("close temporary backup path: %w", err))
		return
	}
	if err := os.Remove(path); err != nil {
		writeAPIError(w, fmt.Errorf("prepare temporary backup path: %w", err))
		return
	}
	defer os.Remove(path)

	if err := a.store.Backup(r.Context(), path); err != nil {
		writeAPIError(w, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeAPIError(w, fmt.Errorf("open database backup: %w", err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeAPIError(w, fmt.Errorf("inspect database backup: %w", err))
		return
	}

	filename := "materialmind-" + time.Now().UTC().Format("20060102-150405") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": filename,
	}))
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
