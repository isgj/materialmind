package mcpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/store"
)

func TestManagerDoesNotShadowModernToolCatalogTTL(t *testing.T) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "ttl-server", Version: "1.0.0"},
		nil,
	)
	addTextTool(t, mcpServer, "inspect", "Inspects a target")
	var listRequests atomic.Int32
	upstreamHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/list"`)) {
			listRequests.Add(1)
		}
		upstreamHandler.ServeHTTP(writer, request)
	}))
	t.Cleanup(upstream.Close)

	manager := New(openTestStore(t), Options{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	configured := []store.SessionMCPServer{{
		MCPServer: store.MCPServer{
			ID:        "ttl-server",
			Name:      "TTL server",
			Transport: store.MCPTransportHTTP,
			URL:       upstream.URL,
			AuthType:  store.MCPAuthNone,
		},
		ConfirmationMode: store.MCPConfirmationAllow,
	}}
	workingDirectory := t.TempDir()
	for range 2 {
		tools, err := manager.SessionTools(
			context.Background(),
			"session-ttl",
			workingDirectory,
			configured,
		)
		if err != nil || len(tools) != 1 {
			t.Fatalf("SessionTools() = %#v, %v", tools, err)
		}
	}
	if got := listRequests.Load(); got != 2 {
		t.Fatalf("tools/list request count = %d, want 2 for ttlMs=0", got)
	}
}

func TestManagerAdvertisesSessionWorkspaceAsMCPRoot(t *testing.T) {
	workspace := t.TempDir()
	rootsSeen := make(chan []*mcp.Root, 1)
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "roots-server", Version: "1.0.0"},
		nil,
	)
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "inspect_roots",
			Description: "Inspects client roots",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			result, err := request.Session.ListRoots(ctx, nil)
			if err != nil {
				return nil, err
			}
			rootsSeen <- result.Roots
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "roots inspected"}},
			}, nil
		},
	)
	upstream := serveMCPTestServer(t, mcpServer)
	manager := New(openTestStore(t), Options{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	definitions, err := manager.SessionTools(
		context.Background(),
		"session-roots",
		workspace,
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "roots-server",
				Name:      "Roots server",
				Transport: store.MCPTransportHTTP,
				URL:       upstream.URL,
				AuthType:  store.MCPAuthNone,
			},
			ConfirmationMode: store.MCPConfirmationAllow,
		}},
	)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("SessionTools() = %#v, %v", definitions, err)
	}
	if _, err := manager.CallTool(context.Background(), definitions[0], map[string]any{}); err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	roots := <-rootsSeen
	want, err := workspaceRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].URI != want.URI || roots[0].Name != want.Name {
		t.Fatalf("roots/list = %#v, want %#v", roots, []*mcp.Root{want})
	}
}

func TestManagerDiscoversReadsAndExpandsSessionContent(t *testing.T) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "content-server", Version: "1.0.0"},
		nil,
	)
	resourceHandler := func(
		_ context.Context,
		request *mcp.ReadResourceRequest,
	) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      request.Params.URI,
			MIMEType: "text/plain",
			Text:     "resource body",
		}}}, nil
	}
	mcpServer.AddResource(&mcp.Resource{
		URI:         "materialmind://guide",
		Name:        "guide",
		Title:       "Workspace guide",
		Description: "Project guidance",
		MIMEType:    "text/plain",
	}, resourceHandler)
	mcpServer.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "materialmind://files/{path}",
		Name:        "file",
		Title:       "Project file",
		MIMEType:    "text/plain",
	}, resourceHandler)
	mcpServer.AddPrompt(&mcp.Prompt{
		Name:        "review",
		Title:       "Review code",
		Description: "Prepare a focused review",
		Arguments: []*mcp.PromptArgument{{
			Name:        "target",
			Description: "Target to review",
			Required:    true,
		}},
	}, func(
		_ context.Context,
		request *mcp.GetPromptRequest,
	) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Expanded review prompt",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: "Review " + request.Params.Arguments["target"]},
			}},
		}, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	t.Cleanup(upstream.Close)

	manager := New(openTestStore(t), Options{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	configured := []store.SessionMCPServer{{
		MCPServer: store.MCPServer{
			ID:        "content-server",
			Name:      "Content server",
			Transport: store.MCPTransportHTTP,
			URL:       upstream.URL,
			AuthType:  store.MCPAuthNone,
		},
		ConfirmationMode: store.MCPConfirmationAllow,
	}}
	content := manager.SessionContent(context.Background(), "session-content", t.TempDir(), configured)
	if len(content) != 1 || content[0].Error != "" ||
		len(content[0].Prompts) != 1 || content[0].Prompts[0].Name != "review" ||
		len(content[0].Resources) != 1 || content[0].Resources[0].URI != "materialmind://guide" ||
		len(content[0].ResourceTemplates) != 1 {
		t.Fatalf("SessionContent() = %#v", content)
	}
	resource, err := manager.ReadSessionResource(
		context.Background(),
		"session-content",
		t.TempDir(),
		"content-server",
		"materialmind://guide",
		configured,
	)
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != "resource body" {
		t.Fatalf("ReadSessionResource() = %#v, %v", resource, err)
	}
	prompt, err := manager.GetSessionPrompt(
		context.Background(),
		"session-content",
		t.TempDir(),
		"content-server",
		"review",
		map[string]string{"target": "internal/engine"},
		configured,
	)
	if err != nil || len(prompt.Messages) != 1 {
		t.Fatalf("GetSessionPrompt() = %#v, %v", prompt, err)
	}
	message, ok := prompt.Messages[0].Content.(map[string]any)
	if !ok || message["type"] != "text" || message["text"] != "Review internal/engine" {
		t.Fatalf("GetSessionPrompt() content = %#v", prompt.Messages[0].Content)
	}
	if _, err := manager.ReadSessionResource(
		context.Background(),
		"session-content",
		t.TempDir(),
		"other-server",
		"materialmind://guide",
		configured,
	); err == nil {
		t.Fatal("ReadSessionResource() accepted a server outside the session")
	}
}

func TestToolUIResourceURI(t *testing.T) {
	tool := &mcp.Tool{Meta: map[string]any{
		"ui": map[string]any{"resourceUri": "ui://materialmind/app"},
	}}
	if got := toolUIResourceURI(tool); got != "ui://materialmind/app" {
		t.Fatalf("toolUIResourceURI() = %q", got)
	}
	tool.Meta["ui"] = map[string]any{"resourceUri": "https://example.test/app"}
	if got := toolUIResourceURI(tool); got != "" {
		t.Fatalf("toolUIResourceURI() accepted non-ui URI %q", got)
	}
}

func TestManagerDiscoversAndCallsStreamableHTTPTools(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "test-token")
	t.Setenv("MCP_TEST_TENANT", "tenant-42")

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)
	addTextTool(t, mcpServer, "zeta", "Zeta tool")
	addTextTool(t, mcpServer, "greet", "Greets a user")
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", authorization)
		}
		if tenant := request.Header.Get("X-Tenant"); tenant != "tenant-42" {
			t.Errorf("X-Tenant = %q, want tenant-42", tenant)
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(upstream.Close)

	dataStore := openTestStore(t)
	manager := New(dataStore, Options{})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	server := store.MCPServer{
		ID:                "server-1",
		Name:              "Test tools",
		Transport:         store.MCPTransportHTTP,
		URL:               upstream.URL,
		Headers:           []store.MCPVariableBinding{{Name: "X-Tenant", ValueEnvVar: "MCP_TEST_TENANT"}},
		AuthType:          store.MCPAuthBearerEnv,
		BearerTokenEnvVar: "MCP_TEST_TOKEN",
	}
	catalog, err := manager.ListServerTools(context.Background(), server)
	if err != nil {
		t.Fatalf("ListServerTools() error = %v", err)
	}
	summaries := catalog.Tools
	if len(summaries) != 2 ||
		summaries[0].Name != "greet" ||
		summaries[0].Description != "Greets a user" ||
		summaries[1].Name != "zeta" {
		t.Fatalf("ListServerTools() = %#v", summaries)
	}
	if catalog.ProtocolVersion == "" || catalog.ServerName != "test-server" || catalog.ServerVersion != "1.0.0" {
		t.Fatalf("ListServerTools() catalog = %#v", catalog)
	}

	definitions, err := manager.SessionTools(
		context.Background(),
		"session-1",
		t.TempDir(),
		[]store.SessionMCPServer{{
			MCPServer:        server,
			ConfirmationMode: store.MCPConfirmationAsk,
			ToolPermissions: []store.MCPToolPermission{{
				ToolName:         "greet",
				ConfirmationMode: store.MCPConfirmationAllow,
			}},
		}},
	)
	if err != nil {
		t.Fatalf("SessionTools() error = %v", err)
	}
	if len(definitions) != 2 {
		t.Fatalf("SessionTools() = %#v", definitions)
	}
	var greet ToolDefinition
	for _, definition := range definitions {
		if definition.OriginalName == "greet" {
			greet = definition
		}
		if !strings.HasPrefix(definition.Name, "mcp_test_tools_") {
			t.Errorf("namespaced tool name = %q", definition.Name)
		}
	}
	if greet.Name == "" || greet.ConfirmationMode != store.MCPConfirmationAllow {
		t.Fatalf("greet definition = %#v", greet)
	}

	result, err := manager.CallTool(
		context.Background(),
		greet,
		map[string]any{"name": "Ada"},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result["state"] != "completed" {
		t.Fatalf("CallTool() result = %#v", result)
	}
	metadata, ok := result["mcp"].(map[string]any)
	if !ok ||
		metadata["serverName"] != "Test tools" ||
		metadata["toolName"] != "greet" {
		t.Fatalf("CallTool() metadata = %#v", result["mcp"])
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("CallTool() content = %#v", result["content"])
	}
	text, ok := content[0].(map[string]any)
	if !ok || text["text"] != "Hello Ada" {
		t.Fatalf("CallTool() text = %#v", content[0])
	}
}

func TestManagerNegotiatesModernProtocolAndHandlesFormElicitation(t *testing.T) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "modern-server", Version: "2.0.0"},
		nil,
	)
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "configure",
			Description: "Configures a target",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(
			_ context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			if response := request.Params.InputResponses["configuration"]; response != nil {
				elicitation, ok := response.(*mcp.ElicitResult)
				if !ok || elicitation.Action != ElicitationActionAccept {
					return nil, fmt.Errorf("unexpected elicitation response %#v", response)
				}
				return &mcp.CallToolResult{
					StructuredContent: map[string]any{"environment": elicitation.Content["environment"]},
				}, nil
			}
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{
					"configuration": &mcp.ElicitParams{
						Mode:    "form",
						Message: "Choose the environment",
						RequestedSchema: map[string]any{
							"type":     "object",
							"required": []string{"environment"},
							"properties": map[string]any{
								"environment": map[string]any{
									"type": "string",
									"enum": []string{"test", "production"},
								},
							},
						},
					},
				},
				RequestState: "configure-state",
			}, nil
		},
	)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	requests := make(chan ElicitationRequest, 1)
	manager := New(openTestStore(t), Options{
		ToolCallTimeout: 200 * time.Millisecond,
		Elicitation: func(
			_ context.Context,
			request ElicitationRequest,
		) (ElicitationResolution, error) {
			requests <- request
			time.Sleep(350 * time.Millisecond)
			return ElicitationResolution{
				ID:         request.ID,
				ToolCallID: request.ToolCallID,
				Action:     ElicitationActionAccept,
				Content:    map[string]any{"environment": "test"},
			}, nil
		},
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	server := store.MCPServer{
		ID:        "modern-server",
		Name:      "Modern server",
		Transport: store.MCPTransportHTTP,
		URL:       upstream.URL,
		AuthType:  store.MCPAuthNone,
	}
	catalog, err := manager.ListServerTools(context.Background(), server)
	if err != nil {
		t.Fatalf("ListServerTools() error = %v", err)
	}
	if catalog.ProtocolVersion != modernProtocolVersion || catalog.ServerName != "modern-server" {
		t.Fatalf("ListServerTools() catalog = %#v", catalog)
	}
	definitions, err := manager.SessionTools(
		context.Background(),
		"session-modern",
		t.TempDir(),
		[]store.SessionMCPServer{{
			MCPServer:        server,
			ConfirmationMode: store.MCPConfirmationAllow,
		}},
	)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("SessionTools() = %#v, %v", definitions, err)
	}
	result, err := manager.CallTool(
		context.Background(),
		definitions[0],
		map[string]any{},
		CallOptions{ToolCallID: "tool-modern"},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	request := <-requests
	if request.SessionID != "session-modern" ||
		request.ToolCallID != "tool-modern" ||
		request.Mode != "form" ||
		request.Message != "Choose the environment" {
		t.Fatalf("elicitation request = %#v", request)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["environment"] != "test" {
		t.Fatalf("CallTool() result = %#v", result)
	}
}

func TestManagerPausesTimeoutForServerInitiatedElicitation(t *testing.T) {
	_, timeout := newCallInactivityTimeout(context.Background(), 30*time.Millisecond)
	call := &activeToolCall{
		sessionID:     "session-legacy",
		toolCallID:    "tool-legacy",
		connectionKey: "session:session-legacy:server-legacy",
		timeout:       timeout,
	}
	manager := &Manager{
		elicitation: func(
			_ context.Context,
			request ElicitationRequest,
		) (ElicitationResolution, error) {
			time.Sleep(75 * time.Millisecond)
			return ElicitationResolution{
				ID:         request.ID,
				ToolCallID: request.ToolCallID,
				Action:     ElicitationActionAccept,
				Content:    map[string]any{"confirmed": true},
			}, nil
		},
		activeCalls: map[string]*activeToolCall{"legacy": call},
	}
	result, err := manager.handleElicitation(
		context.Background(),
		call.connectionKey,
		store.MCPServer{ID: "server-legacy", Name: "Legacy server"},
		&mcp.ElicitRequest{Params: &mcp.ElicitParams{
			Mode:    "form",
			Message: "Confirm the operation",
		}},
	)
	timeout.stop()
	if err != nil {
		t.Fatalf("handleElicitation() error = %v", err)
	}
	if timeout.didTimeOut() {
		t.Fatal("tool call timed out while elicitation was awaiting input")
	}
	if result.Action != ElicitationActionAccept || result.Content["confirmed"] != true {
		t.Fatalf("handleElicitation() = %#v", result)
	}
}

func TestShouldSetLegacyLoggingLevel(t *testing.T) {
	capabilities := &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}}
	if !shouldSetLegacyLoggingLevel(&mcp.InitializeResult{
		ProtocolVersion: "2025-11-25",
		Capabilities:    capabilities,
	}) {
		t.Fatal("legacy logging capability was not enabled")
	}
	if shouldSetLegacyLoggingLevel(&mcp.InitializeResult{
		ProtocolVersion: modernProtocolVersion,
		Capabilities:    capabilities,
	}) {
		t.Fatal("modern protocol attempted to use deprecated logging/setLevel")
	}
}

func TestManagerSkipsUnavailableSessionServer(t *testing.T) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "available-server", Version: "1.0.0"},
		nil,
	)
	addTextTool(t, mcpServer, "greet", "Greets a user")
	available := serveMCPTestServer(t, mcpServer)
	unavailable := httptest.NewServer(http.NotFoundHandler())
	unavailableURL := unavailable.URL
	unavailable.Close()

	events := make(chan Event, 1)
	manager := New(openTestStore(t), Options{
		Events: func(event Event) {
			events <- event
		},
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	definitions, err := manager.SessionTools(
		context.Background(),
		"session-with-unavailable-server",
		t.TempDir(),
		[]store.SessionMCPServer{
			{
				MCPServer: store.MCPServer{
					ID:        "server-unavailable",
					Name:      "Unavailable tools",
					Transport: store.MCPTransportHTTP,
					URL:       unavailableURL,
					AuthType:  store.MCPAuthNone,
				},
				ConfirmationMode: store.MCPConfirmationAsk,
			},
			{
				MCPServer: store.MCPServer{
					ID:        "server-available",
					Name:      "Available tools",
					Transport: store.MCPTransportHTTP,
					URL:       available.URL,
					AuthType:  store.MCPAuthNone,
				},
				ConfirmationMode: store.MCPConfirmationAllow,
			},
		},
	)
	if err != nil {
		t.Fatalf("SessionTools() error = %v", err)
	}
	if len(definitions) != 1 || definitions[0].OriginalName != "greet" {
		t.Fatalf("SessionTools() = %#v", definitions)
	}

	select {
	case event := <-events:
		if event.Type != EventUnavailable ||
			event.SessionID != "session-with-unavailable-server" ||
			event.ServerID != "server-unavailable" ||
			event.ServerName != "Unavailable tools" ||
			event.Error == "" {
			t.Fatalf("unavailable event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("SessionTools() did not report the unavailable server")
	}
}

func TestManagerStreamsMCPCallEventsAndPreservesRichResult(t *testing.T) {
	events := make(chan Event, 16)
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "inspect",
			Title:       "Inspect artifact",
			Description: "Returns several MCP content types",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(
				`{"type":"object","properties":{"count":{"type":"integer"}}}`,
			),
		},
		func(
			ctx context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			token := request.Params.GetProgressToken()
			if err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      1,
				Total:         2,
				Message:       "Reading artifact",
			}); err != nil {
				return nil, err
			}
			logMessage := &mcp.LoggingMessageParams{
				Level:  mcp.LoggingLevel("info"),
				Logger: "artifact-reader",
				Data:   map[string]any{"phase": "read"},
			}
			logMessage.SetProgressToken(token)
			if err := request.Session.Log(ctx, logMessage); err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Artifact ready"},
					&mcp.ImageContent{MIMEType: "image/png", Data: []byte("png")},
					&mcp.ResourceLink{
						URI:      "https://example.test/artifact",
						Name:     "artifact",
						Title:    "Artifact",
						MIMEType: "text/plain",
					},
					&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
						URI:      "file:///artifact.txt",
						MIMEType: "text/plain",
						Text:     "embedded content",
					}},
				},
				StructuredContent: map[string]any{"count": 4},
			}, nil
		},
	)
	upstream := serveMCPTestServer(t, mcpServer)
	manager := New(openTestStore(t), Options{
		Events: func(event Event) {
			events <- event
		},
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	definitions, err := manager.SessionTools(
		context.Background(),
		"session-rich",
		t.TempDir(),
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "server-rich",
				Name:      "Rich tools",
				Transport: store.MCPTransportHTTP,
				URL:       upstream.URL,
				AuthType:  store.MCPAuthNone,
			},
			ConfirmationMode: store.MCPConfirmationAllow,
		}},
	)
	if err != nil {
		t.Fatalf("SessionTools() error = %v", err)
	}
	if len(definitions) != 1 ||
		definitions[0].Title != "Inspect artifact" ||
		definitions[0].OutputSchema == nil {
		t.Fatalf("SessionTools() = %#v", definitions)
	}

	result, err := manager.CallTool(
		context.Background(),
		definitions[0],
		map[string]any{},
		CallOptions{ToolCallID: "call-rich"},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result["state"] != "completed" {
		t.Fatalf("CallTool() result = %#v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 4 {
		t.Fatalf("CallTool() content = %#v", result["content"])
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["count"] != float64(4) {
		t.Fatalf("CallTool() structuredContent = %#v", result["structuredContent"])
	}

	received := receiveEventTypes(
		t,
		events,
		EventCallStarted,
		EventProgress,
		EventLog,
		EventCallFinished,
	)
	if received[EventCallStarted].ToolCallID != "call-rich" ||
		!received[EventCallStarted].Cancelable {
		t.Fatalf("call started event = %#v", received[EventCallStarted])
	}
	if received[EventProgress].Message != "Reading artifact" ||
		received[EventProgress].Progress != 1 ||
		received[EventProgress].Total != 2 {
		t.Fatalf("progress event = %#v", received[EventProgress])
	}
	if received[EventLog].Level != "info" ||
		received[EventLog].Logger != "artifact-reader" ||
		received[EventLog].ToolCallID != "call-rich" {
		t.Fatalf("log event = %#v", received[EventLog])
	}
	finished := received[EventCallFinished]
	if finished.ToolCallID != "call-rich" ||
		finished.Cancelable ||
		finished.Output["state"] != "completed" {
		t.Fatalf("call finished event = %#v", finished)
	}
	finishedContent, ok := finished.Output["content"].([]any)
	if !ok || len(finishedContent) != 4 {
		t.Fatalf("call finished output = %#v", finished.Output)
	}
}

func TestManagerCancelsOneMCPToolCall(t *testing.T) {
	events := make(chan Event, 4)
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "wait",
			Description: "Waits until cancelled",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(
			ctx context.Context,
			_ *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	upstream := serveMCPTestServer(t, mcpServer)
	manager := New(openTestStore(t), Options{
		Events: func(event Event) {
			events <- event
		},
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	definitions, err := manager.SessionTools(
		context.Background(),
		"session-cancel",
		t.TempDir(),
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "server-cancel",
				Name:      "Cancellation tools",
				Transport: store.MCPTransportHTTP,
				URL:       upstream.URL,
				AuthType:  store.MCPAuthNone,
			},
			ConfirmationMode: store.MCPConfirmationAllow,
		}},
	)
	if err != nil {
		t.Fatalf("SessionTools() error = %v", err)
	}

	callResult := make(chan map[string]any, 1)
	callError := make(chan error, 1)
	go func() {
		result, callErr := manager.CallTool(
			context.Background(),
			definitions[0],
			map[string]any{},
			CallOptions{ToolCallID: "call-cancel"},
		)
		callResult <- result
		callError <- callErr
	}()
	started := receiveEventTypes(t, events, EventCallStarted)[EventCallStarted]
	if started.ToolCallID != "call-cancel" {
		t.Fatalf("call started event = %#v", started)
	}
	if !manager.CancelToolCall("session-cancel", "call-cancel") {
		t.Fatal("CancelToolCall() = false, want true")
	}
	select {
	case err := <-callError:
		if err != nil {
			t.Fatalf("CallTool() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled MCP call")
	}
	result := <-callResult
	if result["state"] != "cancelled" {
		t.Fatalf("CallTool() result = %#v", result)
	}
	if manager.CancelToolCall("session-cancel", "call-cancel") {
		t.Fatal("CancelToolCall() after completion = true, want false")
	}
}

func TestManagerTimesOutUnresponsiveMCPToolCall(t *testing.T) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "wait",
			Description: "Waits until cancelled",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(
			ctx context.Context,
			_ *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	upstream := serveMCPTestServer(t, mcpServer)
	manager := New(openTestStore(t), Options{ToolCallTimeout: 50 * time.Millisecond})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	definitions, err := manager.SessionTools(
		context.Background(),
		"session-timeout",
		t.TempDir(),
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "server-timeout",
				Name:      "Timeout tools",
				Transport: store.MCPTransportHTTP,
				URL:       upstream.URL,
				AuthType:  store.MCPAuthNone,
			},
			ConfirmationMode: store.MCPConfirmationAllow,
		}},
	)
	if err != nil {
		t.Fatalf("SessionTools() error = %v", err)
	}

	started := time.Now()
	result, err := manager.CallTool(
		context.Background(),
		definitions[0],
		map[string]any{},
		CallOptions{ToolCallID: "call-timeout"},
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("CallTool() elapsed = %s, want a prompt timeout", elapsed)
	}
	if result["state"] != "timed_out" ||
		result["timedOut"] != true ||
		!strings.Contains(fmt.Sprint(result["error"]), "timed out") {
		t.Fatalf("CallTool() result = %#v", result)
	}
	metadata, ok := result["mcp"].(map[string]any)
	if !ok || metadata["isError"] != true {
		t.Fatalf("CallTool() metadata = %#v", result["mcp"])
	}
	if manager.CancelToolCall("session-timeout", "call-timeout") {
		t.Fatal("CancelToolCall() after timeout = true, want false")
	}
}

func TestManagerRefreshesSessionToolsAfterListChange(t *testing.T) {
	events := make(chan Event, 8)
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "1.0.0"},
		nil,
	)
	addTextTool(t, mcpServer, "alpha", "Alpha tool")
	upstream := serveMCPTestServer(t, mcpServer)
	manager := New(openTestStore(t), Options{
		Events: func(event Event) {
			events <- event
		},
	})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	server := store.MCPServer{
		ID:        "server-changing",
		Name:      "Changing tools",
		Transport: store.MCPTransportHTTP,
		URL:       upstream.URL,
		AuthType:  store.MCPAuthNone,
	}
	configured := []store.SessionMCPServer{{
		MCPServer:        server,
		ConfirmationMode: store.MCPConfirmationAllow,
	}}
	initial, err := manager.SessionTools(
		context.Background(),
		"session-changing",
		t.TempDir(),
		configured,
	)
	if err != nil {
		t.Fatalf("initial SessionTools() error = %v", err)
	}
	if len(initial) != 1 || initial[0].OriginalName != "alpha" {
		t.Fatalf("initial SessionTools() = %#v", initial)
	}

	addTextTool(t, mcpServer, "beta", "Beta tool")
	changed := receiveEventTypes(t, events, EventToolsChanged)[EventToolsChanged]
	if len(changed.Added) != 1 ||
		changed.Added[0] != "beta" ||
		len(changed.Removed) != 0 ||
		changed.Count != 2 {
		t.Fatalf("tools changed event = %#v", changed)
	}
	refreshed, err := manager.SessionTools(
		context.Background(),
		"session-changing",
		t.TempDir(),
		configured,
	)
	if err != nil {
		t.Fatalf("refreshed SessionTools() error = %v", err)
	}
	if len(refreshed) != 2 ||
		refreshed[0].OriginalName != "alpha" ||
		refreshed[1].OriginalName != "beta" {
		t.Fatalf("refreshed SessionTools() = %#v", refreshed)
	}
}

func TestChangedToolNames(t *testing.T) {
	added, removed := changedToolNames(
		[]*mcp.Tool{{Name: "alpha"}, {Name: "removed"}},
		[]*mcp.Tool{{Name: "alpha"}, {Name: "beta"}},
	)
	if len(added) != 1 || added[0] != "beta" {
		t.Fatalf("added = %v", added)
	}
	if len(removed) != 1 || removed[0] != "removed" {
		t.Fatalf("removed = %v", removed)
	}
}

func TestCloseServerCancelsConnectionInProgress(t *testing.T) {
	requestStarted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			return
		}
		select {
		case <-requestStarted:
		default:
			close(requestStarted)
		}
		<-request.Context().Done()
	}))
	defer upstream.Close()
	defer upstream.CloseClientConnections()

	manager := New(openTestStore(t), Options{})
	server := store.MCPServer{
		ID:        "blocked-server",
		Name:      "Blocked server",
		Transport: store.MCPTransportHTTP,
		URL:       upstream.URL,
		AuthType:  store.MCPAuthNone,
	}
	result := make(chan error, 1)
	go func() {
		_, err := manager.ListServerTools(context.Background(), server)
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MCP request")
	}
	manager.CloseServer(server.ID)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("ListServerTools() error = nil after CloseServer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListServerTools() remained blocked after CloseServer")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestHeaderRoundTripperCloseCancelsActiveRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	requestFinished := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		request *http.Request,
	) {
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			return
		}
		close(requestStarted)
		<-request.Context().Done()
		close(requestFinished)
	}))
	defer upstream.Close()
	defer upstream.CloseClientConnections()

	connectionContext, cancelConnection := context.WithCancel(context.Background())
	defer cancelConnection()
	transport := &headerRoundTripper{
		base:       http.DefaultTransport,
		headers:    make(http.Header),
		connection: connectionContext,
		active:     make(map[*activeHTTPRequest]struct{}),
	}
	requestResult := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			upstream.URL,
			strings.NewReader(`{"jsonrpc":"2.0"}`),
		)
		if err != nil {
			requestResult <- err
			return
		}
		response, err := (&http.Client{Transport: transport}).Do(request)
		if response != nil {
			response.Body.Close()
		}
		requestResult <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
	}
	transport.close()
	select {
	case <-requestFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("server request remained active after round tripper close")
	}
	select {
	case err := <-requestResult:
		if err == nil {
			t.Fatal("HTTP request error = nil after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request remained blocked after round tripper close")
	}
}

func addTextTool(t *testing.T, server *mcp.Server, name, description string) {
	t.Helper()
	server.AddTool(
		&mcp.Tool{
			Name:        name,
			Description: description,
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"name":{"type":"string"}}}`,
			),
		},
		func(
			_ context.Context,
			request *mcp.CallToolRequest,
		) (*mcp.CallToolResult, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "Hello " + input.Name}},
			}, nil
		},
	)
}

func serveMCPTestServer(t *testing.T, server *mcp.Server) *httptest.Server {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true},
	)
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	return upstream
}

func receiveEventTypes(t *testing.T, events <-chan Event, types ...string) map[string]Event {
	t.Helper()
	received := make(map[string]Event, len(types))
	wanted := make(map[string]struct{}, len(types))
	for _, eventType := range types {
		wanted[eventType] = struct{}{}
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for len(received) < len(wanted) {
		select {
		case event := <-events:
			if _, ok := wanted[event.Type]; ok {
				received[event.Type] = event
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for MCP events %v; received %#v", types, received)
		}
	}
	return received
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dataStore, err := store.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "materialmind.db"),
	)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := dataStore.Close(); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return dataStore
}
