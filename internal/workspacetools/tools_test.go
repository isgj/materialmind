package workspacetools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

type fetchTestContext struct {
	agent.ContextMock
	actions        session.EventActions
	confirmation   *toolconfirmation.ToolConfirmation
	functionCallID string
	hint           string
	payload        any
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (c *fetchTestContext) Actions() *session.EventActions {
	return &c.actions
}

func (c *fetchTestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return c.confirmation
}

func (c *fetchTestContext) FunctionCallID() string {
	return c.functionCallID
}

func (c *fetchTestContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *fetchTestContext) Done() <-chan struct{} {
	return nil
}

func (c *fetchTestContext) Err() error {
	return nil
}

func (c *fetchTestContext) Value(any) any {
	return nil
}

func (c *fetchTestContext) RequestConfirmation(hint string, payload any) error {
	c.hint = hint
	c.payload = payload
	return nil
}

func TestReadReturnsRequestedLines(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := read(root, ReadFileArgs{Path: "notes.txt", StartLine: 2, EndLine: 3})
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if result.Content != "two\nthree\n" || result.StartLine != 2 || result.EndLine != 3 {
		t.Fatalf("read() = %#v", result)
	}
}

func TestReadReturnsRangeBeyondInitialOutputLimit(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	for line := 1; line <= 40_000; line++ {
		fmt.Fprintf(&content, "line %05d\n", line)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := read(root, ReadFileArgs{Path: "large.txt", StartLine: 39_999, EndLine: 40_000})
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if result.Content != "line 39999\nline 40000\n" || result.StartLine != 39_999 || result.EndLine != 40_000 {
		t.Fatalf("read() = %#v", result)
	}
}

func TestReadCannotEscapeWorkspace(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(root, ReadFileArgs{Path: "../outside.txt"}); err == nil {
		t.Fatal("read() traversal error = nil")
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := read(root, ReadFileArgs{Path: "outside-link"}); err == nil {
		t.Fatal("read() outside symlink error = nil")
	}
}

func TestReadRepositoryScopeAllowsWorkspaceSibling(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(repository, "services", "api")
	shared := filepath.Join(repository, "services", "shared")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "config.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeRepository)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolReadFile,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeRepository,
	}, nil, ReadFileArgs{Path: "../shared/config.txt"})
	if err != nil || result.Content != "shared\n" || result.Path != "../shared/config.txt" {
		t.Fatalf("readWithPolicy() = %#v, %v", result, err)
	}
}

func TestReadAskRequestsApprovalInsideScopeOnly(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	permission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolReadFile,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}
	ctx := &fetchTestContext{}
	result, err := readWithPolicy(access, permission, ctx, ReadFileArgs{Path: "notes.txt"})
	if err != nil || result.State != "approval_required" || ctx.payload == nil {
		t.Fatalf("readWithPolicy() = %#v, %v, payload %#v", result, err, ctx.payload)
	}
	outsideContext := &fetchTestContext{}
	if _, err := readWithPolicy(access, permission, outsideContext, ReadFileArgs{Path: "../outside.txt"}); err == nil {
		t.Fatal("readWithPolicy() outside error = nil")
	}
	if outsideContext.payload != nil {
		t.Fatalf("out-of-scope read requested approval: %#v", outsideContext.payload)
	}
}

func TestReadRejectsBinaryFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := read(root, ReadFileArgs{Path: "binary"})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("read() error = %v, want binary error", err)
	}
}

func TestListDirectorySortsEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta.txt", "Alpha.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := list(root, ListDirectoryArgs{Path: "."})
	if err != nil {
		t.Fatalf("list() error = %v", err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Name != "Alpha.txt" {
		t.Fatalf("list() entries = %#v", result.Entries)
	}
}

func TestFetchURLRequestsApprovalBeforeFetching(t *testing.T) {
	ctx := &fetchTestContext{}
	result, err := fetchURL(ctx, FetchURLArgs{URL: "https://example.com/docs#section"})
	if err != nil {
		t.Fatalf("fetchURL() error = %v", err)
	}
	if result.State != "approval_required" || result.URL != "https://example.com/docs" {
		t.Fatalf("fetchURL() = %#v", result)
	}
	if !strings.Contains(ctx.hint, result.URL) || ctx.payload == nil {
		t.Fatalf("confirmation hint = %q, payload = %#v", ctx.hint, ctx.payload)
	}
	if !ctx.actions.SkipSummarization {
		t.Fatal("fetchURL() did not skip summarization while awaiting approval")
	}
}

func TestFetchURLReturnsDenialReason(t *testing.T) {
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: false,
		Payload:   map[string]any{"reason": "Use the internal documentation instead."},
	}}
	result, err := fetchURL(ctx, FetchURLArgs{URL: "https://example.com/docs"})
	if err != nil {
		t.Fatalf("fetchURL() error = %v", err)
	}
	if result.State != "denied" || result.Reason != "Use the internal documentation instead." {
		t.Fatalf("fetchURL() = %#v", result)
	}
}

func TestFetchURLRunsApprovedRequest(t *testing.T) {
	previousClient := defaultFetchClient
	defaultFetchClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("approved response")),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { defaultFetchClient = previousClient })

	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{Confirmed: true}}
	result, err := fetchURL(ctx, FetchURLArgs{URL: "https://example.com/docs"})
	if err != nil {
		t.Fatalf("fetchURL() error = %v", err)
	}
	if result.State != "fetched" || result.Content != "approved response" {
		t.Fatalf("fetchURL() = %#v", result)
	}
}

func TestFetchURLOriginRuleSkipsApproval(t *testing.T) {
	previousClient := defaultFetchClient
	defaultFetchClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("trusted")),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { defaultFetchClient = previousClient })

	ctx := &fetchTestContext{}
	result, err := fetchURLWithPolicy(toolpolicy.Permission{
		ToolName:         toolpolicy.ToolFetchURL,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		TargetRules: []toolpolicy.TargetRule{{
			Matcher:          toolpolicy.TargetOrigin,
			Target:           "https://example.com",
			ConfirmationMode: toolpolicy.ConfirmationAllow,
		}},
	}, ctx, FetchURLArgs{URL: "https://example.com/docs"})
	if err != nil || result.State != "fetched" || result.Content != "trusted" {
		t.Fatalf("fetchURLWithPolicy() = %#v, %v", result, err)
	}
	if ctx.payload != nil {
		t.Fatalf("trusted fetch requested approval: %#v", ctx.payload)
	}
}

func TestFetchURLRedirectEvaluatesTargetPolicy(t *testing.T) {
	previousClient := defaultFetchClient
	requestCount := 0
	defaultFetchClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://other.example/next"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { defaultFetchClient = previousClient })

	ctx := &fetchTestContext{}
	result, err := fetchURLWithPolicy(toolpolicy.Permission{
		ToolName:         toolpolicy.ToolFetchURL,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		TargetRules: []toolpolicy.TargetRule{{
			Matcher:          toolpolicy.TargetOrigin,
			Target:           "https://example.com",
			ConfirmationMode: toolpolicy.ConfirmationAllow,
		}},
	}, ctx, FetchURLArgs{URL: "https://example.com/start"})
	if err != nil || result.State != "approval_required" || result.URL != "https://other.example/next" {
		t.Fatalf("fetchURLWithPolicy() = %#v, %v", result, err)
	}
	if requestCount != 1 {
		t.Fatalf("redirect request count = %d, want 1", requestCount)
	}
	payload, ok := ctx.payload.(fetchConfirmationPayload)
	if !ok || payload.URL != result.URL || len(payload.ApprovedURLs) != 1 {
		t.Fatalf("redirect approval payload = %#v", ctx.payload)
	}
}

func TestFetchContentReturnsBoundedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.Repeat("x", maxFetchBytes+10)))
	}))
	defer server.Close()

	result, err := fetchContent(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("fetchContent() error = %v", err)
	}
	if result.State != "fetched" || result.HTTPStatus != http.StatusOK || !result.Truncated {
		t.Fatalf("fetchContent() = %#v", result)
	}
	if len(result.Content) != maxFetchBytes {
		t.Fatalf("len(fetchContent().Content) = %d, want %d", len(result.Content), maxFetchBytes)
	}
}

func TestFetchURLRejectsUnsafeDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"file:///tmp/secret",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://10.0.0.1",
		"https://user:password@example.com",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := normalizeFetchURL(rawURL); err == nil {
				t.Fatalf("normalizeFetchURL(%q) error = nil", rawURL)
			}
		})
	}
	if !isPublicAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("isPublicAddress(8.8.8.8) = false")
	}
	if isPublicAddress(netip.MustParseAddr("100.64.0.1")) {
		t.Fatal("isPublicAddress(100.64.0.1) = true")
	}
}
