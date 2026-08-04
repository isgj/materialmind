package acpruntime

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestClientRoutesElicitationByMetadataSession(t *testing.T) {
	client := newClient()
	first := &recordingACPHandler{}
	second := &recordingACPHandler{}
	client.register(context.Background(), "first", "", first)
	client.register(context.Background(), "second", "", second)

	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{
		Properties: map[string]any{"answer": map[string]any{"type": "string"}},
	})
	request.Form.Meta = map[string]any{"sessionId": "second"}
	response, err := client.UnstableCreateElicitation(context.Background(), request)
	if err != nil {
		t.Fatalf("UnstableCreateElicitation() error = %v", err)
	}
	if response.Accept == nil || second.elicitationCount() != 1 || first.elicitationCount() != 0 {
		t.Fatalf(
			"UnstableCreateElicitation() response = %#v, counts = (%d, %d)",
			response,
			first.elicitationCount(),
			second.elicitationCount(),
		)
	}
}

func TestClientCancelsAmbiguousElicitation(t *testing.T) {
	client := newClient()
	client.register(context.Background(), "first", "", &recordingACPHandler{})
	client.register(context.Background(), "second", "", &recordingACPHandler{})
	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{})

	response, err := client.UnstableCreateElicitation(context.Background(), request)
	if err != nil {
		t.Fatalf("UnstableCreateElicitation() error = %v", err)
	}
	if response.Cancel == nil {
		t.Fatalf("UnstableCreateElicitation() = %#v, want cancel", response)
	}
}

func TestClientRoutesURLCompletionToRequestHandler(t *testing.T) {
	client := newClient()
	handler := &recordingACPHandler{}
	client.register(context.Background(), "session", "", handler)
	request := acp.NewUnstableCreateElicitationRequestUrl(
		"login-1",
		"https://example.com/authorize",
	)
	request.Url.Message = "Sign in"

	response, err := client.UnstableCreateElicitation(context.Background(), request)
	if err != nil {
		t.Fatalf("UnstableCreateElicitation() error = %v", err)
	}
	if response.Accept == nil {
		t.Fatalf("UnstableCreateElicitation() = %#v, want accept", response)
	}
	if err := client.UnstableCompleteElicitation(
		context.Background(),
		acp.UnstableCompleteElicitationNotification{ElicitationId: "login-1"},
	); err != nil {
		t.Fatalf("UnstableCompleteElicitation() error = %v", err)
	}
	if handler.completionCount() != 1 {
		t.Fatalf("completion count = %d, want 1", handler.completionCount())
	}
}

func TestSafeElicitationURL(t *testing.T) {
	for _, rawURL := range []string{
		"javascript:alert(1)",
		"https://user:secret@example.com/authorize",
		"/relative",
	} {
		if _, err := safeElicitationURL(rawURL); err == nil {
			t.Errorf("safeElicitationURL(%q) succeeded, want error", rawURL)
		}
	}
	got, err := safeElicitationURL("HTTPS://example.com/authorize")
	if err != nil || got != "https://example.com/authorize" {
		t.Fatalf("safeElicitationURL() = %q, %v", got, err)
	}
}
