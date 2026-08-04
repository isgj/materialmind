package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"materialmind/internal/agentmodel"
	"materialmind/internal/credentialstore"
	"materialmind/internal/engine"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
)

func TestStorageMaintenanceEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataStore.Close() })
	handler := New(dataStore, nil)

	var settings store.StorageSettings
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/storage-settings",
		nil,
		http.StatusOK,
		&settings,
	)
	if settings.RetentionDays != 0 {
		t.Fatalf("default settings = %#v", settings)
	}
	requestJSON(
		t,
		handler,
		http.MethodPut,
		"/api/storage-settings",
		store.StorageSettings{RetentionDays: 90},
		http.StatusOK,
		&settings,
	)
	if settings.RetentionDays != 90 {
		t.Fatalf("updated settings = %#v", settings)
	}
	requestJSON(
		t,
		handler,
		http.MethodPut,
		"/api/storage-settings",
		store.StorageSettings{RetentionDays: -1},
		http.StatusBadRequest,
		nil,
	)

	request := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/backup status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/vnd.sqlite3" ||
		recorder.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(recorder.Header().Get("Content-Disposition"), "materialmind-") {
		t.Fatalf("backup headers = %#v", recorder.Header())
	}
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(backupPath, recorder.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	backupStore, err := store.Open(ctx, backupPath)
	if err != nil {
		t.Fatalf("open downloaded backup: %v", err)
	}
	defer backupStore.Close()
	backupSettings, err := backupStore.GetStorageSettings(ctx)
	if err != nil || backupSettings.RetentionDays != 90 {
		t.Fatalf("backup settings = %#v, %v", backupSettings, err)
	}
}

func TestTranscriptPageEndpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dataStore.CreateACPAgent(ctx, "Test agent", "missing-test-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := dataStore.CreateACPSession(ctx, workspace.ID, "Transcript", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"one", "two", "three", "four", "five"} {
		_, err := dataStore.UpsertACPTranscriptItem(ctx, sessionRecord.ID, store.TranscriptItem{
			ID:        id,
			Kind:      "message",
			Role:      "assistant",
			Text:      id,
			CreatedAt: time.Date(2026, time.July, 1, 12, index, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	runEngine := engine.New(dataStore)
	handler := New(dataStore, runEngine)

	var latest engine.TranscriptPage
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/transcript-page?limit=2",
		nil,
		http.StatusOK,
		&latest,
	)
	if !latest.HasMore || latest.NextCursor == nil || *latest.NextCursor != 3 ||
		len(latest.Items) != 2 || latest.Items[0].ID != "four" {
		t.Fatalf("latest page = %#v", latest)
	}
	var older engine.TranscriptPage
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/transcript-page?limit=2&before=3",
		nil,
		http.StatusOK,
		&older,
	)
	if older.NextCursor == nil || *older.NextCursor != 1 || older.Items[0].ID != "two" {
		t.Fatalf("older page = %#v", older)
	}
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/transcript-page?limit=0",
		nil,
		http.StatusBadRequest,
		nil,
	)
}

func TestSessionNotesEndpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dataStore.CreateACPAgent(ctx, "Test agent", "missing-test-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := dataStore.CreateACPSession(ctx, workspace.ID, "Notes", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(dataStore, nil)

	var notes store.SessionNotes
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/notes",
		nil,
		http.StatusOK,
		&notes,
	)
	if notes.SessionID != sessionRecord.ID || notes.Content != "" || notes.Revision != 0 {
		t.Fatalf("empty notes = %#v", notes)
	}

	updated, changed, err := dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, "# Review\n\nCheck auth.", 0)
	if err != nil || !changed {
		t.Fatalf("update notes = %#v, %v, changed=%v", updated, err, changed)
	}
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/notes",
		nil,
		http.StatusOK,
		&notes,
	)
	if notes.Content != "# Review\n\nCheck auth." || notes.Revision != 1 {
		t.Fatalf("updated notes = %#v", notes)
	}
}

func TestBearerToken(t *testing.T) {
	for _, test := range []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer scoped-token", want: "scoped-token", ok: true},
		{header: "bearer scoped-token", want: "scoped-token", ok: true},
		{header: "Basic scoped-token"},
		{header: "Bearer"},
	} {
		got, ok := bearerToken(test.header)
		if got != test.want || ok != test.ok {
			t.Fatalf("bearerToken(%q) = %q, %t", test.header, got, ok)
		}
	}
}

func TestLLMProviderKeyringCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" ||
			r.Header.Get("Authorization") != "Bearer keyring-token" {
			t.Errorf(
				"upstream request = %s, authorization = %q",
				r.URL.Path,
				r.Header.Get("Authorization"),
			)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(upstream.Close)

	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	credentials := credentialstore.NewMemory()
	runEngine := engine.NewWithOptions(dataStore, engine.Options{Credentials: credentials})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runEngine.Shutdown(shutdownContext); err != nil {
			t.Errorf("engine.Shutdown() error = %v", err)
		}
		dataStore.Close()
	})
	handler := New(dataStore, runEngine)

	var provider store.LLMProvider
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/llm-providers",
		map[string]any{
			"name":             "Keyring gateway",
			"apiCompatibility": "openai-chat-completions",
			"baseUrl":          upstream.URL + "/v1",
			"authType":         store.LLMAuthBearerKeyring,
			"bearerToken":      "keyring-token",
		},
		http.StatusCreated,
		&provider,
	)
	if provider.AuthType != store.LLMAuthBearerKeyring ||
		!provider.CredentialAvailable ||
		provider.CredentialBackend != "memory" ||
		provider.BearerTokenEnvVar != "" {
		t.Fatalf("created provider = %#v", provider)
	}
	token, err := credentials.Get(credentialstore.LLMProviderTokenKey(provider.ID))
	if err != nil || token != "keyring-token" {
		t.Fatalf("stored credential = %q, %v", token, err)
	}
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/llm-providers/"+provider.ID+"/available-models",
		nil,
		http.StatusOK,
		&[]agentmodel.AvailableModel{},
	)

	requestJSON(
		t,
		handler,
		http.MethodPatch,
		"/api/llm-providers/"+provider.ID,
		map[string]any{
			"name":             "Renamed keyring gateway",
			"apiCompatibility": provider.APICompatibility,
			"baseUrl":          provider.BaseURL,
			"authType":         store.LLMAuthBearerKeyring,
			"bearerToken":      "",
		},
		http.StatusOK,
		&provider,
	)
	token, err = credentials.Get(credentialstore.LLMProviderTokenKey(provider.ID))
	if err != nil || token != "keyring-token" {
		t.Fatalf("preserved credential = %q, %v", token, err)
	}

	requestJSON(
		t,
		handler,
		http.MethodPatch,
		"/api/llm-providers/"+provider.ID,
		map[string]any{
			"name":             provider.Name,
			"apiCompatibility": provider.APICompatibility,
			"baseUrl":          provider.BaseURL,
			"authType":         store.LLMAuthNone,
		},
		http.StatusOK,
		&provider,
	)
	if provider.AuthType != store.LLMAuthNone || !provider.CredentialAvailable {
		t.Fatalf("provider without authentication = %#v", provider)
	}
	if _, err := credentials.Get(credentialstore.LLMProviderTokenKey(provider.ID)); !errors.Is(
		err,
		credentialstore.ErrNotFound,
	) {
		t.Fatalf("removed credential error = %v, want not found", err)
	}
}

func TestToolPermissionEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repositoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repositoryRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(repositoryRoot, "packages", "app")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", workspaceRoot)
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	handler := New(dataStore, nil)

	workspaceResponse := requestToolPermissions(
		t,
		handler,
		http.MethodGet,
		"/api/workspaces/"+workspace.ID+"/tool-permissions",
		nil,
	)
	if workspaceResponse.OwnerType != "workspace" || workspaceResponse.RepositoryRoot != repositoryRoot {
		t.Fatalf("workspace permission response = %#v", workspaceResponse)
	}
	if len(workspaceResponse.Definitions) != len(toolpolicy.Definitions()) {
		t.Fatalf("tool definitions = %#v", workspaceResponse.Definitions)
	}
	if _, ok := toolpolicy.PermissionFor(workspaceResponse.Permissions, toolpolicy.ToolLoadSkill); !ok {
		t.Fatalf("load_skill permission is missing from %#v", workspaceResponse.Permissions)
	}
	permissions := workspaceResponse.Permissions
	for index := range permissions {
		if permissions[index].ToolName == toolpolicy.ToolFetchURL {
			permissions[index].TargetRules = []toolpolicy.TargetRule{{
				Matcher:          toolpolicy.TargetOrigin,
				Target:           "https://DOCS.example.test:443/guides",
				ConfirmationMode: toolpolicy.ConfirmationAllow,
			}}
		}
	}
	workspaceResponse = requestToolPermissions(
		t,
		handler,
		http.MethodPut,
		"/api/workspaces/"+workspace.ID+"/tool-permissions",
		toolPermissionRequest{Permissions: permissions},
	)
	fetchPermission, ok := toolpolicy.PermissionFor(workspaceResponse.Permissions, toolpolicy.ToolFetchURL)
	if !ok || len(fetchPermission.TargetRules) != 1 || fetchPermission.TargetRules[0].Target != "https://docs.example.test" {
		t.Fatalf("normalized fetch permission = %#v", fetchPermission)
	}

	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Review", nil)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionResponse := requestToolPermissions(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/tool-permissions",
		nil,
	)
	if sessionResponse.OwnerType != "session" || sessionResponse.OwnerName != "Review" || sessionResponse.SessionStatus != "idle" {
		t.Fatalf("session permission response = %#v", sessionResponse)
	}
	inheritedFetch, ok := toolpolicy.PermissionFor(sessionResponse.Permissions, toolpolicy.ToolFetchURL)
	if !ok || len(inheritedFetch.TargetRules) != 1 || inheritedFetch.TargetRules[0] != fetchPermission.TargetRules[0] {
		t.Fatalf("inherited fetch permission = %#v", inheritedFetch)
	}
}

func TestMCPConfigurationEndpoints(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	handler := New(dataStore, nil)

	var server store.MCPServer
	requestJSON(
		t,
		handler,
		http.MethodPost,
		"/api/mcp-servers",
		map[string]any{
			"name":              "Context server",
			"transport":         "http",
			"url":               "https://mcp.example.test/mcp",
			"authType":          "bearer_env",
			"bearerTokenEnvVar": "PROJECT_MCP_TOKEN",
		},
		http.StatusCreated,
		&server,
	)
	if server.ID == "" ||
		server.Name != "Context server" ||
		server.Transport != store.MCPTransportHTTP ||
		server.CredentialAvailable {
		t.Fatalf("created MCP server = %#v", server)
	}

	requestJSON(
		t,
		handler,
		http.MethodPut,
		"/api/mcp-servers/"+server.ID+"/defaults",
		map[string]any{
			"enabled":          true,
			"confirmationMode": store.MCPConfirmationAllow,
			"toolPermissions": []map[string]any{{
				"toolName":         "lookup",
				"confirmationMode": store.MCPConfirmationAsk,
			}},
		},
		http.StatusOK,
		&server,
	)
	if !server.DefaultEnabled ||
		server.DefaultConfirmationMode != store.MCPConfirmationAllow ||
		len(server.DefaultToolPermissions) != 1 {
		t.Fatalf("updated MCP server defaults = %#v", server)
	}
	inheritedWorkspace, err := dataStore.CreateWorkspace(ctx, "Inherited project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() with MCP defaults error = %v", err)
	}
	var inheritedAssignments []store.MCPServerAssignment
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/workspaces/"+inheritedWorkspace.ID+"/mcp-servers",
		nil,
		http.StatusOK,
		&inheritedAssignments,
	)
	if len(inheritedAssignments) != 1 ||
		!inheritedAssignments[0].Enabled ||
		inheritedAssignments[0].ConfirmationMode != store.MCPConfirmationAllow ||
		len(inheritedAssignments[0].ToolPermissions) != 1 {
		t.Fatalf("inherited workspace MCP assignments = %#v", inheritedAssignments)
	}

	var workspaceAssignments []store.MCPServerAssignment
	requestJSON(
		t,
		handler,
		http.MethodPut,
		"/api/workspaces/"+workspace.ID+"/mcp-servers",
		map[string]any{
			"assignments": []map[string]any{{
				"serverId":         server.ID,
				"enabled":          true,
				"confirmationMode": store.MCPConfirmationAsk,
				"toolPermissions": []map[string]any{{
					"toolName":         "lookup",
					"confirmationMode": store.MCPConfirmationAllow,
				}},
			}},
		},
		http.StatusOK,
		&workspaceAssignments,
	)
	if len(workspaceAssignments) != 1 ||
		!workspaceAssignments[0].Enabled ||
		len(workspaceAssignments[0].ToolPermissions) != 1 {
		t.Fatalf("workspace MCP assignments = %#v", workspaceAssignments)
	}

	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Review", nil)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	var sessionAssignments []store.MCPServerAssignment
	requestJSON(
		t,
		handler,
		http.MethodGet,
		"/api/sessions/"+sessionRecord.ID+"/mcp-servers",
		nil,
		http.StatusOK,
		&sessionAssignments,
	)
	if len(sessionAssignments) != 1 ||
		!sessionAssignments[0].Enabled ||
		sessionAssignments[0].ConfirmationMode != store.MCPConfirmationAsk {
		t.Fatalf("session MCP assignments = %#v", sessionAssignments)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete,
		"/api/mcp-servers/"+server.ID,
		nil,
	))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("DELETE assigned MCP server status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAvailableLLMModelsEndpoint(t *testing.T) {
	ctx := context.Background()
	const environmentName = "MATERIALMIND_HTTP_MODEL_LIST_TOKEN"
	t.Setenv(environmentName, "catalog-token")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer catalog-token" {
			t.Errorf("upstream request = %s, authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"provider/model","object":"model","created":1,"owned_by":"provider"}]}`)
	}))
	t.Cleanup(upstream.Close)

	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	provider, err := dataStore.CreateLLMProvider(
		ctx,
		"OpenAI compatible",
		"openai-chat-completions",
		upstream.URL+"/v1",
		environmentName,
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	handler := New(dataStore, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/api/llm-providers/"+provider.ID+"/available-models",
		nil,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"ownedBy"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response) != 1 || response[0].ID != "provider/model" || response[0].OwnedBy != "provider" {
		t.Fatalf("response = %#v", response)
	}
}

func TestStartRunAppliesReasoningEffortOverride(t *testing.T) {
	ctx := context.Background()
	upstreamRequests := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer run-token" {
			t.Errorf("upstream authorization = %q", authorization)
		}
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode upstream request: %v", err)
		} else {
			upstreamRequests <- requestBody
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp-1","object":"response","created_at":1,"status":"completed","model":"gpt-test","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done","annotations":[]}]}],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":3}}`)
	}))

	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	credentials := credentialstore.NewMemory()
	runEngine := engine.NewWithOptions(dataStore, engine.Options{Credentials: credentials})
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runEngine.Shutdown(shutdownContext); err != nil {
			t.Errorf("engine.Shutdown() error = %v", err)
		}
		dataStore.Close()
		upstream.Close()
	})

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProviderWithAuth(
		ctx,
		"OpenAI Responses",
		"openai-responses",
		upstream.URL+"/v1",
		store.LLMAuthBearerKeyring,
		"",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	if err := credentials.Set(
		credentialstore.LLMProviderTokenKey(provider.ID),
		"run-token",
	); err != nil {
		t.Fatalf("save provider credential: %v", err)
	}
	configuredEffort := "high"
	modelRecord, err := dataStore.CreateLLMModel(ctx, provider.ID, "GPT Test", "gpt-test", store.GenerationSettings{
		MaxOutputTokens: 4096,
		ReasoningEffort: &configuredEffort,
	})
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}
	sessionRecord, err := runEngine.CreateSession(ctx, workspace.ID, "Review", &modelRecord.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	handler := New(dataStore, runEngine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+sessionRecord.ID+"/runs",
		bytes.NewBufferString(`{"message":"Reply with Done","llmModelId":"`+modelRecord.ID+`","reasoningEffort":"low"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var run store.Run
	if err := json.Unmarshal(recorder.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if run.ReasoningEffort == nil || *run.ReasoningEffort != "low" {
		t.Fatalf("run reasoning effort = %#v, want low", run.ReasoningEffort)
	}

	select {
	case requestBody := <-upstreamRequests:
		reasoning, ok := requestBody["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "low" {
			t.Fatalf("upstream reasoning = %#v", requestBody["reasoning"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream request")
	}
}

func TestDecodeStartRunRequestWithAttachments(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message", "Review the attached context"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("llmModelId", "model-1"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("files", "context.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "package context\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/session-1/runs", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	decoded, ok := decodeStartRunRequest(recorder, request)

	if !ok {
		t.Fatalf("decodeStartRunRequest() status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if decoded.Message != "Review the attached context" ||
		decoded.LLMModelID != "model-1" ||
		len(decoded.Attachments) != 1 {
		t.Fatalf("decodeStartRunRequest() = %#v", decoded)
	}
	attachment := decoded.Attachments[0]
	if attachment.Name != "context.go" ||
		attachment.MIMEType != "text/plain" ||
		string(attachment.Content) != "package context\n" {
		t.Fatalf("attachment = %#v", attachment)
	}
}

func TestDecodeJSONValidatesRequest(t *testing.T) {
	t.Parallel()

	oversized := append([]byte(`{"name":"`), bytes.Repeat([]byte("a"), maxRequestBody)...)
	oversized = append(oversized, '"', '}')
	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "requires JSON content type", body: []byte(`{}`), wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "rejects unknown field", body: []byte(`{"unknown":true}`), contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "rejects multiple values", body: []byte(`{} {}`), contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "rejects oversized body", body: oversized, contentType: "application/json", wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			recorder := httptest.NewRecorder()
			var destination struct {
				Name string `json:"name"`
			}
			if decodeJSON(recorder, request, &destination) {
				t.Fatal("decodeJSON() = true, want false")
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			var response errorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", response.Error.Code, test.wantCode)
			}
		})
	}
}

func requestToolPermissions(t *testing.T, handler http.Handler, method, target string, body any) toolPermissionResponse {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status = %d, body = %s", method, target, response.StatusCode, message)
	}
	var result toolPermissionResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func requestJSON(
	t *testing.T,
	handler http.Handler,
	method, target string,
	body any,
	wantStatus int,
	result any,
) {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf(
			"%s %s status = %d, want %d, body = %s",
			method,
			target,
			recorder.Code,
			wantStatus,
			recorder.Body.String(),
		)
	}
	if result != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), result); err != nil {
			t.Fatalf("decode %s %s response: %v", method, target, err)
		}
	}
}
