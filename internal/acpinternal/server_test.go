package acpinternal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/workspacetools"
)

func TestInternalMCPExposesTypedSessionNotesTools(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request BrokerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.ToolName != ToolReadSessionNotes {
			t.Errorf("tool name = %q", request.ToolName)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"output": workspacetools.ReadSessionNotesResult{
				State:    "read",
				Content:  "# Durable note",
				Revision: 4,
			},
		})
	}))
	t.Cleanup(broker.Close)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- newServer(broker.URL, "scoped-token").Run(ctx, serverTransport) }()
	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0.0"},
		nil,
	)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 2 || tools.Tools[0].Name != ToolReadSessionNotes ||
		tools.Tools[1].Name != ToolUpdateSessionNotes {
		t.Fatalf("tools = %#v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      ToolReadSessionNotes,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	output, ok := result.StructuredContent.(map[string]any)
	if result.IsError || !ok || output["state"] != "read" || output["revision"] != float64(4) {
		t.Fatalf("CallTool() = %#v", result)
	}
}

func TestCallBrokerAuthenticatesAndDecodesTypedOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Errorf("request = %s, authorization = %q", r.Method, r.Header.Get("Authorization"))
		}
		var request BrokerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.ToolName != ToolUpdateSessionNotes ||
			string(request.Arguments) != `{"content":"# Notes","expectedRevision":2}` {
			t.Errorf("broker request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"output": workspacetools.UpdateSessionNotesResult{
				State:    "updated",
				Revision: 3,
				Bytes:    7,
			},
		})
	}))
	t.Cleanup(server.Close)

	result, err := callBroker[workspacetools.UpdateSessionNotesResult](
		t.Context(),
		server.URL,
		"scoped-token",
		ToolUpdateSessionNotes,
		workspacetools.UpdateSessionNotesArgs{Content: "# Notes", ExpectedRevision: 2},
	)
	if err != nil {
		t.Fatalf("callBroker() error = %v", err)
	}
	if result.State != "updated" || result.Revision != 3 || result.Bytes != 7 {
		t.Fatalf("callBroker() = %#v", result)
	}
}

func TestCallBrokerReturnsBackendErrorToTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "the ACP session has no active run"},
		})
	}))
	t.Cleanup(server.Close)

	_, err := callBroker[workspacetools.ReadSessionNotesResult](
		t.Context(), server.URL, "scoped-token", ToolReadSessionNotes,
		workspacetools.ReadSessionNotesArgs{},
	)
	if err == nil || !strings.Contains(err.Error(), "the ACP session has no active run") {
		t.Fatalf("callBroker() error = %v", err)
	}
}
