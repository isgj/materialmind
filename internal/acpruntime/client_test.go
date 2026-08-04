package acpruntime

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestClientDelegatesFilesystemRequestsToActiveSessionHandler(t *testing.T) {
	client := newClient()
	handler := &filesystemACPHandler{}
	client.register(context.Background(), "session-1", "/workspace", handler)

	read, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
		SessionId: "session-1",
		Path:      "/workspace/example.txt",
	})
	if err != nil || read.Content != "read content" {
		t.Fatalf("ReadTextFile() = %#v, %v", read, err)
	}
	if _, err := client.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		SessionId: "session-1",
		Path:      "/workspace/example.txt",
		Content:   "updated content",
	}); err != nil {
		t.Fatalf("WriteTextFile() error = %v", err)
	}
	if handler.written != "updated content" {
		t.Fatalf("WriteTextFile() content = %q", handler.written)
	}
	if _, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{
		SessionId: "unknown",
		Path:      "/workspace/example.txt",
	}); err == nil {
		t.Fatal("ReadTextFile() without an active handler returned nil error")
	}
}

type filesystemACPHandler struct {
	written string
}

func (*filesystemACPHandler) SessionUpdate(context.Context, acp.SessionNotification) error {
	return nil
}

func (*filesystemACPHandler) RequestPermission(
	context.Context,
	acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeCancelled(),
	}, nil
}

func (*filesystemACPHandler) ReadTextFile(
	context.Context,
	acp.ReadTextFileRequest,
) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{Content: "read content"}, nil
}

func (h *filesystemACPHandler) WriteTextFile(
	_ context.Context,
	request acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	h.written = request.Content
	return acp.WriteTextFileResponse{}, nil
}
