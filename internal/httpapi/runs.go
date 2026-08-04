package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"materialmind/internal/engine"
	"materialmind/internal/store"
)

func (a *API) transcript(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.Transcript(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) transcriptPage(w http.ResponseWriter, r *http.Request) {
	const (
		defaultLimit = 100
		maxLimit     = 500
	)

	limit := defaultLimit
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maxLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	var before *int
	if value := r.URL.Query().Get("before"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "before must be a non-negative cursor")
			return
		}
		before = &parsed
	}
	page, err := a.engine.TranscriptPage(r.Context(), r.PathValue("id"), before, limit)
	writeResult(w, page, err)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListRuns(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeStartRunRequest(w, r)
	if !ok {
		return
	}
	run, err := a.engine.StartRun(
		r.Context(),
		r.PathValue("id"),
		request.LLMModelID,
		request.Message,
		store.RunGenerationOverrides{ReasoningEffort: request.ReasoningEffort},
		request.Attachments,
	)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) getRunAttachment(w http.ResponseWriter, r *http.Request) {
	attachment, err := a.store.GetRunAttachment(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	disposition := "attachment"
	if strings.HasPrefix(attachment.MIMEType, "image/") {
		disposition = "inline"
	}
	if value := mime.FormatMediaType(disposition, map[string]string{
		"filename": attachment.Name,
	}); value != "" {
		w.Header().Set("Content-Disposition", value)
	}
	w.Header().Set("Content-Type", attachment.MIMEType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(
		w,
		r,
		attachment.Name,
		attachment.CreatedAt,
		bytes.NewReader(attachment.Content),
	)
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.engine.CancelRun(r.Context(), r.PathValue("id"))
	writeResult(w, run, err)
}

func (a *API) cancelMCPToolCall(w http.ResponseWriter, r *http.Request) {
	cancellation, err := a.engine.CancelMCPToolCall(
		r.Context(),
		r.PathValue("id"),
		r.PathValue("toolCallID"),
	)
	writeResult(w, cancellation, err)
}

func (a *API) resolveMCPElicitation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	resolution, err := a.engine.ResolveMCPElicitation(
		r.Context(),
		r.PathValue("id"),
		r.PathValue("requestID"),
		request.Action,
		request.Content,
	)
	writeResult(w, resolution, err)
}

func (a *API) resolveToolApproval(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
		OptionID string `json:"optionId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	resolution, err := a.engine.ResolveToolApproval(
		r.Context(),
		r.PathValue("id"),
		r.PathValue("approvalID"),
		request.Approved,
		request.Reason,
		request.OptionID,
	)
	writeResult(w, resolution, err)
}

func (a *API) resolveUserInput(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Answers []engine.UserInputAnswerSubmission `json:"answers"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	resolution, err := a.engine.ResolveUserInput(
		r.Context(),
		r.PathValue("id"),
		r.PathValue("requestID"),
		request.Answers,
	)
	writeResult(w, resolution, err)
}

func (a *API) streamRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, err := a.store.GetRun(r.Context(), runID); err != nil {
		writeAPIError(w, err)
		return
	}
	after := parseEventSequence(r)
	subscription, ok := a.engine.Hub().SubscribeReplay(r.Context(), runID, after)
	if !ok {
		writeError(w, http.StatusGone, "stream is no longer available; reload the transcript")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if subscription.Truncated {
		reset := engine.StreamEvent{
			Sequence: subscription.OldestSequence - 1,
			Type:     "stream_reset",
			Data: map[string]any{
				"reason":         "replay history was truncated",
				"oldestSequence": subscription.OldestSequence,
			},
		}
		data, err := engine.EncodeStreamEvent(reset)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(
			w,
			"id: %d\nevent: %s\ndata: %s\n\n",
			reset.Sequence,
			reset.Type,
			data,
		); err != nil {
			return
		}
		flusher.Flush()
	}

	keepAlive := time.NewTicker(engine.StreamKeepAliveInterval())
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-subscription.Events:
			if !open {
				return
			}
			data, err := engine.EncodeStreamEvent(event)
			if err != nil {
				slog.Error("encode stream event", "run_id", runID, "error", err)
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func parseEventSequence(r *http.Request) int64 {
	value := r.Header.Get("Last-Event-ID")
	if query := r.URL.Query().Get("after"); query != "" {
		value = query
	}
	sequence, _ := strconv.ParseInt(value, 10, 64)
	return max(sequence, 0)
}

func decodeStartRunRequest(
	w http.ResponseWriter,
	r *http.Request,
) (startRunRequest, bool) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		var request startRunRequest
		return request, decodeJSON(w, r, &request)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartRunSize)
	if err := r.ParseMultipartForm(maxRunAttachmentsSize); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "attachments exceed the 25 MiB total limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart request: "+err.Error())
		}
		return startRunRequest{}, false
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	request := startRunRequest{
		Message:    r.FormValue("message"),
		LLMModelID: r.FormValue("llmModelId"),
	}
	if reasoningEffort := strings.TrimSpace(r.FormValue("reasoningEffort")); reasoningEffort != "" {
		request.ReasoningEffort = &reasoningEffort
	}
	files := r.MultipartForm.File["files"]
	if len(files) > maxRunAttachmentCount {
		writeError(w, http.StatusBadRequest, "a message can contain at most 10 attachments")
		return startRunRequest{}, false
	}

	var totalSize int64
	request.Attachments = make([]store.RunAttachment, 0, len(files))
	for _, fileHeader := range files {
		attachment, err := readRunAttachment(fileHeader)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return startRunRequest{}, false
		}
		totalSize += attachment.Size
		if totalSize > maxRunAttachmentsSize {
			writeError(w, http.StatusRequestEntityTooLarge, "attachments exceed the 25 MiB total limit")
			return startRunRequest{}, false
		}
		request.Attachments = append(request.Attachments, attachment)
	}
	return request, true
}

func readRunAttachment(fileHeader *multipart.FileHeader) (store.RunAttachment, error) {
	name := path.Base(strings.ReplaceAll(fileHeader.Filename, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return store.RunAttachment{}, fmt.Errorf("attachment filename is required")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return store.RunAttachment{}, fmt.Errorf("open attachment %q: %w", name, err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxRunAttachmentSize+1))
	if err != nil {
		return store.RunAttachment{}, fmt.Errorf("read attachment %q: %w", name, err)
	}
	if len(content) > maxRunAttachmentSize {
		return store.RunAttachment{}, fmt.Errorf("attachment %q exceeds the 10 MiB limit", name)
	}
	mimeType, err := supportedAttachmentMIMEType(
		fileHeader.Header.Get("Content-Type"),
		content,
	)
	if err != nil {
		return store.RunAttachment{}, fmt.Errorf("attachment %q: %w", name, err)
	}
	return store.RunAttachment{
		Name:     name,
		MIMEType: mimeType,
		Size:     int64(len(content)),
		Content:  content,
	}, nil
}

func supportedAttachmentMIMEType(declared string, content []byte) (string, error) {
	declared, _, _ = mime.ParseMediaType(declared)
	detected := http.DetectContentType(content)
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		return detected, nil
	}
	if utf8.Valid(content) && !bytes.ContainsRune(content, '\x00') {
		if isTextMediaType(declared) {
			return declared, nil
		}
		return "text/plain", nil
	}
	return "", fmt.Errorf(
		"must be UTF-8 text, PDF, PNG, JPEG, GIF, or WebP",
	)
}

func isTextMediaType(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	if !strings.HasPrefix(mediaType, "application/") {
		return false
	}
	subtype := strings.TrimPrefix(mediaType, "application/")
	return subtype == "json" ||
		subtype == "javascript" ||
		subtype == "sql" ||
		subtype == "toml" ||
		subtype == "xml" ||
		subtype == "yaml" ||
		subtype == "x-javascript" ||
		subtype == "x-sh" ||
		subtype == "x-yaml" ||
		strings.HasSuffix(subtype, "+json") ||
		strings.HasSuffix(subtype, "+xml")
}
