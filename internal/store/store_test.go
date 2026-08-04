package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"materialmind/internal/toolpolicy"
)

func TestACPAgentSessionRunAndTranscript(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	agentRecord, err := dataStore.CreateACPAgent(
		ctx,
		"Test ACP agent",
		executable,
		[]string{"--acp-test"},
	)
	if err != nil {
		t.Fatalf("CreateACPAgent() error = %v", err)
	}
	if !agentRecord.Available || agentRecord.ResolvedCommand == "" {
		t.Fatalf("CreateACPAgent() availability = %#v", agentRecord)
	}
	agentRecord, err = dataStore.UpdateACPAgent(
		ctx,
		agentRecord.ID,
		"Updated ACP agent",
		executable,
		[]string{"--acp-test", "--verbose"},
	)
	if err != nil {
		t.Fatalf("UpdateACPAgent() error = %v", err)
	}
	if agentRecord.Name != "Updated ACP agent" || len(agentRecord.Arguments) != 2 {
		t.Fatalf("UpdateACPAgent() = %#v", agentRecord)
	}
	agents, err := dataStore.ListACPAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].ID != agentRecord.ID {
		t.Fatalf("ListACPAgents() = %#v, %v", agents, err)
	}

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	sessionRecord, err := dataStore.CreateACPSession(
		ctx,
		workspace.ID,
		"ACP review",
		agentRecord.ID,
	)
	if err != nil {
		t.Fatalf("CreateACPSession() error = %v", err)
	}
	if sessionRecord.RuntimeType != RuntimeACP ||
		sessionRecord.ACPAgentID == nil ||
		*sessionRecord.ACPAgentID != agentRecord.ID ||
		string(sessionRecord.ACPConfigOptions) != "[]" {
		t.Fatalf("CreateACPSession() = %#v", sessionRecord)
	}
	configOptions := json.RawMessage(
		`[{"type":"boolean","id":"fast","name":"Fast mode","currentValue":true}]`,
	)
	sessionRecord, err = dataStore.UpdateACPSessionConnection(
		ctx,
		sessionRecord.ID,
		"agent-session-1",
		configOptions,
	)
	if err != nil {
		t.Fatalf("UpdateACPSessionConnection() error = %v", err)
	}
	if sessionRecord.ACPSessionID != "agent-session-1" ||
		string(sessionRecord.ACPConfigOptions) != string(configOptions) {
		t.Fatalf("UpdateACPSessionConnection() = %#v", sessionRecord)
	}

	run, err := dataStore.CreateACPRunWithAttachments(
		ctx,
		sessionRecord.ID,
		"Inspect the workspace",
		[]RunAttachment{{
			Name:     "context.txt",
			MIMEType: "text/plain",
			Content:  []byte("attached context"),
		}},
	)
	if err != nil {
		t.Fatalf("CreateACPRunWithAttachments() error = %v", err)
	}
	if run.RuntimeType != RuntimeACP ||
		run.ACPAgentID != agentRecord.ID ||
		run.ACPAgentName != agentRecord.Name ||
		run.InvocationID != run.ID ||
		len(run.Attachments) != 1 ||
		run.Attachments[0].Name != "context.txt" ||
		run.Attachments[0].Size != int64(len("attached context")) {
		t.Fatalf("CreateACPRunWithAttachments() = %#v", run)
	}
	persistedRun, err := dataStore.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if persistedRun.RuntimeType != RuntimeACP ||
		persistedRun.ACPAgentID != agentRecord.ID ||
		len(persistedRun.Attachments) != 1 ||
		len(persistedRun.Attachments[0].Content) != 0 {
		t.Fatalf("GetRun() = %#v", persistedRun)
	}
	attachment, err := dataStore.GetRunAttachment(ctx, run.Attachments[0].ID)
	if err != nil {
		t.Fatalf("GetRunAttachment() error = %v", err)
	}
	if string(attachment.Content) != "attached context" {
		t.Fatalf("GetRunAttachment() = %#v", attachment)
	}

	thought := TranscriptItem{
		ID:           run.ID + ":thought",
		InvocationID: run.InvocationID,
		Kind:         "thought",
		Role:         "assistant",
		Text:         "Inspecting",
		Model:        agentRecord.Name,
	}
	if _, err := dataStore.UpsertACPTranscriptItem(ctx, sessionRecord.ID, thought); err != nil {
		t.Fatalf("UpsertACPTranscriptItem() error = %v", err)
	}
	thought.Text = "Inspecting the project"
	if _, err := dataStore.UpsertACPTranscriptItem(ctx, sessionRecord.ID, thought); err != nil {
		t.Fatalf("UpsertACPTranscriptItem() update error = %v", err)
	}
	plan := TranscriptItem{
		ID:           run.ID + ":plan",
		InvocationID: run.InvocationID,
		Kind:         "plan",
		Role:         "assistant",
		Text:         "Plan\n- [>] Verify the implementation",
		ToolOutput: map[string]any{
			"entries": []map[string]any{
				{
					"content":  "Verify the implementation",
					"priority": "medium",
					"status":   "in_progress",
				},
			},
		},
		Model: agentRecord.Name,
	}
	if _, err := dataStore.UpsertACPTranscriptItem(ctx, sessionRecord.ID, plan); err != nil {
		t.Fatalf("UpsertACPTranscriptItem() plan error = %v", err)
	}
	transcript, err := dataStore.ListACPTranscript(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("ListACPTranscript() error = %v", err)
	}
	if len(transcript) != 3 ||
		transcript[0].Role != "user" ||
		transcript[0].Text != run.UserMessage ||
		transcript[1].Kind != "thought" ||
		transcript[1].Text != thought.Text ||
		transcript[2].Kind != "plan" ||
		transcript[2].ToolOutput["entries"] == nil {
		t.Fatalf("ListACPTranscript() = %#v", transcript)
	}

	if err := dataStore.DeleteACPAgent(ctx, agentRecord.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteACPAgent() in-use error = %v, want conflict", err)
	}
	if err := dataStore.DeleteSession(ctx, sessionRecord.ID); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if err := dataStore.DeleteACPAgent(ctx, agentRecord.ID); err != nil {
		t.Fatalf("DeleteACPAgent() error = %v", err)
	}
}

func TestStoreCRUDAndEmptyCollections(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("PROJECT_CLAUDE_TOKEN", "test-token")
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	workspaces, err := dataStore.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces() error = %v", err)
	}
	if workspaces == nil || len(workspaces) != 0 {
		t.Fatalf("ListWorkspaces() = %#v, want non-nil empty slice", workspaces)
	}
	sessions, err := dataStore.ListAllSessions(ctx)
	if err != nil {
		t.Fatalf("ListAllSessions() error = %v", err)
	}
	if sessions == nil || len(sessions) != 0 {
		t.Fatalf("ListAllSessions() = %#v, want non-nil empty slice", sessions)
	}

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", root)
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	if workspace.RootPath != root || !workspace.Available {
		t.Fatalf("CreateWorkspace() = %#v", workspace)
	}
	if _, err := dataStore.CreateWorkspace(ctx, "Duplicate", root); err == nil {
		t.Fatal("CreateWorkspace() duplicate error = nil")
	}

	provider, err := dataStore.CreateLLMProvider(
		ctx, "Claude Gateway", "anthropic", "https://claude.example.test/api", "PROJECT_CLAUDE_TOKEN",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	if provider.BaseURL != "https://claude.example.test/api" ||
		provider.AuthType != LLMAuthBearerEnv ||
		provider.BearerTokenEnvVar != "PROJECT_CLAUDE_TOKEN" ||
		!provider.CredentialAvailable {
		t.Fatalf("CreateLLMProvider() = %#v", provider)
	}
	modelRecord, err := dataStore.CreateLLMModel(ctx, provider.ID, "Claude Test", "claude-test", GenerationSettings{
		ContextWindowTokens: 4_000_000,
		MaxOutputTokens:     2_000_000,
	})
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}
	if _, err := dataStore.CreateLLMModel(ctx, provider.ID, "Claude Small", "claude-small", GenerationSettings{MaxOutputTokens: 4096}); err != nil {
		t.Fatalf("CreateLLMModel() second model error = %v", err)
	}
	if err := dataStore.DeleteLLMProvider(ctx, provider.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteLLMProvider() error = %v, want conflict", err)
	}
	models, err := dataStore.ListLLMModels(ctx)
	if err != nil || len(models) != 2 {
		t.Fatalf("ListLLMModels() = %#v, %v", models, err)
	}
	appSession, err := dataStore.CreateSession(ctx, workspace.ID, "Review", &modelRecord.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessions, err = dataStore.ListAllSessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].ID != appSession.ID {
		t.Fatalf("ListAllSessions() = %#v, %v", sessions, err)
	}
	runs, err := dataStore.ListRuns(ctx, appSession.ID)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if runs == nil || len(runs) != 0 {
		t.Fatalf("ListRuns() = %#v, want non-nil empty slice", runs)
	}

	run, err := dataStore.CreateRun(ctx, appSession.ID, modelRecord.ID, "Inspect the project", RunGenerationOverrides{})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if run.APICompatibility != "anthropic" || run.ModelID != "claude-test" ||
		run.ContextWindowTokens != 4_000_000 || run.MaxOutputTokens != 2_000_000 ||
		run.LLMProviderID != provider.ID || run.LLMModelID != modelRecord.ID ||
		run.BaseURL != provider.BaseURL || run.BearerTokenEnvVar != provider.BearerTokenEnvVar {
		t.Fatalf("CreateRun() snapshot = %#v", run)
	}
	reasoningEffort := "high"
	reasoningRun, err := dataStore.CreateRun(
		ctx,
		appSession.ID,
		modelRecord.ID,
		"Reason about the project",
		RunGenerationOverrides{ReasoningEffort: &reasoningEffort},
	)
	if err != nil {
		t.Fatalf("CreateRun() Anthropic reasoning override error = %v", err)
	}
	if reasoningRun.ReasoningEffort == nil || *reasoningRun.ReasoningEffort != reasoningEffort {
		t.Fatalf(
			"CreateRun() Anthropic reasoning effort = %#v, want %q",
			reasoningRun.ReasoningEffort,
			reasoningEffort,
		)
	}
	completed, err := dataStore.UpdateRun(ctx, run.ID, "completed", "invocation-1", "")
	if err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	if completed.Status != "completed" || completed.CompletedAt == nil {
		t.Fatalf("UpdateRun() = %#v", completed)
	}
}

func TestOpenReusesCurrentSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "materialmind.db")
	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	workspace, err := first.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()
	stored, err := second.GetWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspace() after reopen error = %v", err)
	}
	if stored.Name != workspace.Name || stored.RootPath != workspace.RootPath {
		t.Fatalf("workspace after reopen = %#v, want %#v", stored, workspace)
	}
}

func TestLLMProviderAndModelValidation(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	if _, err := dataStore.CreateLLMProvider(
		ctx, "Invalid URL", "anthropic", "ftp://claude.example.test", "",
	); err == nil {
		t.Fatal("CreateLLMProvider() invalid URL error = nil")
	}
	if _, err := dataStore.CreateLLMProvider(
		ctx, "Invalid environment", "anthropic", "", "not-valid",
	); err == nil {
		t.Fatal("CreateLLMProvider() invalid environment variable error = nil")
	}
	if _, err := dataStore.CreateLLMProviderWithAuth(
		ctx,
		"Missing environment",
		"anthropic",
		"",
		LLMAuthBearerEnv,
		"",
	); err == nil {
		t.Fatal("CreateLLMProviderWithAuth() missing environment variable error = nil")
	}
	keyringProvider, err := dataStore.CreateLLMProviderWithAuth(
		ctx,
		"Keyring",
		"anthropic",
		"",
		LLMAuthBearerKeyring,
		"IGNORED_ENVIRONMENT",
	)
	if err != nil {
		t.Fatalf("CreateLLMProviderWithAuth() error = %v", err)
	}
	if keyringProvider.AuthType != LLMAuthBearerKeyring ||
		keyringProvider.BearerTokenEnvVar != "" ||
		keyringProvider.CredentialAvailable {
		t.Fatalf("keyring provider = %#v", keyringProvider)
	}

	const missingEnvironment = "MATERIALMIND_NOT_SET"
	t.Setenv(missingEnvironment, "")
	provider, err := dataStore.CreateLLMProvider(
		ctx, "Missing environment", "anthropic", "", missingEnvironment,
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	if provider.CredentialAvailable {
		t.Fatal("CreateLLMProvider() credentialAvailable = true")
	}
	openAIProvider, err := dataStore.CreateLLMProvider(
		ctx, "OpenAI compatible", "openai-chat", "https://gateway.example.test/v1", "",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() OpenAI error = %v", err)
	}
	if openAIProvider.APICompatibility != "openai-chat-completions" {
		t.Fatalf("CreateLLMProvider() compatibility = %q", openAIProvider.APICompatibility)
	}
	responsesProvider, err := dataStore.CreateLLMProvider(
		ctx, "OpenAI Responses", "responses", "https://responses.example.test/v1", "",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() Responses error = %v", err)
	}
	if responsesProvider.APICompatibility != "openai-responses" {
		t.Fatalf("CreateLLMProvider() Responses compatibility = %q", responsesProvider.APICompatibility)
	}
	geminiProvider, err := dataStore.CreateLLMProvider(
		ctx, "Gemini", "google-gemini", "https://generativelanguage.example.test", "",
	)
	if err != nil {
		t.Fatalf("CreateLLMProvider() Gemini error = %v", err)
	}
	if geminiProvider.APICompatibility != "gemini" {
		t.Fatalf("CreateLLMProvider() Gemini compatibility = %q", geminiProvider.APICompatibility)
	}
	if _, err := dataStore.CreateLLMProviderWithAuth(
		ctx,
		"Gemini without API key",
		"gemini",
		"",
		LLMAuthNone,
		"",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateLLMProvider() Gemini without API key error = %v, want invalid input", err)
	}
	if _, err := dataStore.CreateLLMModel(ctx, provider.ID, "Invalid limit", "claude-test", GenerationSettings{}); err == nil {
		t.Fatal("CreateLLMModel() invalid max output tokens error = nil")
	}
	if _, err := dataStore.CreateLLMModel(ctx, provider.ID, "Invalid context", "claude-test", GenerationSettings{
		ContextWindowTokens: 2048,
		MaxOutputTokens:     4096,
	}); err == nil {
		t.Fatal("CreateLLMModel() context below max output error = nil")
	}
	unsupportedReasoningEffort := "extreme"
	if _, err := dataStore.CreateLLMModel(ctx, responsesProvider.ID, "Invalid reasoning", "gpt-test", GenerationSettings{
		MaxOutputTokens: 4096,
		ReasoningEffort: &unsupportedReasoningEffort,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateLLMModel() invalid reasoning effort error = %v, want invalid input", err)
	}
}

func TestGenerationSettingsSchemaExcludesUnsupportedColumns(t *testing.T) {
	dataStore, err := Open(context.Background(), filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	for _, table := range []string{"llm_models", "runs"} {
		var count int
		err := dataStore.DB().QueryRow(`SELECT count(*)
			FROM pragma_table_info(?)
			WHERE name IN ('temperature', 'top_p', 'top_k', 'stop_sequences_json')`, table).Scan(&count)
		if err != nil {
			t.Fatalf("inspect %s schema: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s contains %d unsupported generation-setting columns", table, count)
		}
	}
}

func TestReasoningEffortPersistsAndIsSnapshottedOntoRuns(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(ctx, "OpenAI Responses", "openai-responses", "", "")
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	high := "high"
	modelRecord, err := dataStore.CreateLLMModel(ctx, provider.ID, "GPT Test", "gpt-test", GenerationSettings{
		MaxOutputTokens: 8192,
		ReasoningEffort: &high,
	})
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}
	if modelRecord.ReasoningEffort == nil || *modelRecord.ReasoningEffort != high {
		t.Fatalf("CreateLLMModel() reasoning effort = %#v", modelRecord.ReasoningEffort)
	}
	session, err := dataStore.CreateSession(ctx, workspace.ID, "Review", &modelRecord.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	run, err := dataStore.CreateRun(ctx, session.ID, modelRecord.ID, "Inspect the project", RunGenerationOverrides{})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	low := "low"
	if _, err := dataStore.UpdateLLMModel(ctx, modelRecord.ID, provider.ID, modelRecord.Name, modelRecord.ModelID, GenerationSettings{
		MaxOutputTokens: 8192,
		ReasoningEffort: &low,
	}); err != nil {
		t.Fatalf("UpdateLLMModel() error = %v", err)
	}
	persistedRun, err := dataStore.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if persistedRun.ReasoningEffort == nil || *persistedRun.ReasoningEffort != high {
		t.Fatalf("GetRun() reasoning effort = %#v, want %q", persistedRun.ReasoningEffort, high)
	}
	minimal := "minimal"
	overriddenRun, err := dataStore.CreateRun(
		ctx,
		session.ID,
		modelRecord.ID,
		"Use less reasoning",
		RunGenerationOverrides{ReasoningEffort: &minimal},
	)
	if err != nil {
		t.Fatalf("CreateRun() with reasoning override error = %v", err)
	}
	if overriddenRun.ReasoningEffort == nil || *overriddenRun.ReasoningEffort != minimal {
		t.Fatalf("CreateRun() reasoning override = %#v, want %q", overriddenRun.ReasoningEffort, minimal)
	}
	unsupported := "extreme"
	if _, err := dataStore.CreateRun(
		ctx,
		session.ID,
		modelRecord.ID,
		"Use unsupported reasoning",
		RunGenerationOverrides{ReasoningEffort: &unsupported},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRun() invalid reasoning override error = %v, want invalid input", err)
	}
	updatedModel, err := dataStore.GetLLMModel(ctx, modelRecord.ID)
	if err != nil {
		t.Fatalf("GetLLMModel() error = %v", err)
	}
	if updatedModel.ReasoningEffort == nil || *updatedModel.ReasoningEffort != low {
		t.Fatalf("GetLLMModel() reasoning effort = %#v, want %q", updatedModel.ReasoningEffort, low)
	}
}

func TestAnthropicReasoningEffortValidation(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	provider, err := dataStore.CreateLLMProvider(ctx, "Claude", "anthropic", "", "")
	if err != nil {
		t.Fatalf("CreateLLMProvider() error = %v", err)
	}
	medium := "medium"
	modelRecord, err := dataStore.CreateLLMModel(
		ctx,
		provider.ID,
		"Claude Test",
		"claude-test",
		GenerationSettings{MaxOutputTokens: 8192, ReasoningEffort: &medium},
	)
	if err != nil {
		t.Fatalf("CreateLLMModel() error = %v", err)
	}
	ultra := "ultra"
	if _, err := dataStore.CreateLLMModel(
		ctx,
		provider.ID,
		"Unsupported Claude",
		"claude-unsupported",
		GenerationSettings{MaxOutputTokens: 8192, ReasoningEffort: &ultra},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateLLMModel() ultra effort error = %v, want invalid input", err)
	}

	session, err := dataStore.CreateSession(ctx, workspace.ID, "Review", &modelRecord.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	low := "low"
	run, err := dataStore.CreateRun(
		ctx,
		session.ID,
		modelRecord.ID,
		"Review the project",
		RunGenerationOverrides{ReasoningEffort: &low},
	)
	if err != nil {
		t.Fatalf("CreateRun() low effort error = %v", err)
	}
	if run.ReasoningEffort == nil || *run.ReasoningEffort != low {
		t.Fatalf("CreateRun() reasoning effort = %#v, want %q", run.ReasoningEffort, low)
	}
	if _, err := dataStore.CreateRun(
		ctx,
		session.ID,
		modelRecord.ID,
		"Review with unsupported effort",
		RunGenerationOverrides{ReasoningEffort: &ultra},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRun() ultra effort error = %v, want invalid input", err)
	}
}

func TestToolPermissionsInheritAndRemainIndependent(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	permissions, err := dataStore.GetWorkspaceToolPermissions(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceToolPermissions() error = %v", err)
	}
	read := permissionByName(t, permissions, toolpolicy.ToolReadFile)
	if read.ConfirmationMode != toolpolicy.ConfirmationAllow || read.FilesystemScope != toolpolicy.ScopeWorkspace {
		t.Fatalf("default read permission = %#v", read)
	}
	grep := permissionByName(t, permissions, toolpolicy.ToolGrep)
	if grep.ConfirmationMode != toolpolicy.ConfirmationAllow || grep.FilesystemScope != toolpolicy.ScopeWorkspace {
		t.Fatalf("default grep permission = %#v", grep)
	}
	loadSkill := permissionByName(t, permissions, toolpolicy.ToolLoadSkill)
	if loadSkill.ConfirmationMode != toolpolicy.ConfirmationAllow || loadSkill.FilesystemScope != "" {
		t.Fatalf("default load skill permission = %#v", loadSkill)
	}
	for index := range permissions {
		switch permissions[index].ToolName {
		case toolpolicy.ToolReadFile:
			permissions[index].FilesystemScope = toolpolicy.ScopeRepository
		case toolpolicy.ToolFetchURL:
			permissions[index].TargetRules = []toolpolicy.TargetRule{{
				Matcher:          toolpolicy.TargetOrigin,
				Target:           "https://DOCS.example.test:443/reference",
				ConfirmationMode: toolpolicy.ConfirmationAllow,
			}}
		}
	}
	workspacePermissions, err := dataStore.ReplaceWorkspaceToolPermissions(ctx, workspace.ID, permissions)
	if err != nil {
		t.Fatalf("ReplaceWorkspaceToolPermissions() error = %v", err)
	}

	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Review", nil)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionPermissions, err := dataStore.GetSessionToolPermissions(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("GetSessionToolPermissions() error = %v", err)
	}
	if got := permissionByName(t, sessionPermissions, toolpolicy.ToolReadFile).FilesystemScope; got != toolpolicy.ScopeRepository {
		t.Fatalf("inherited read scope = %q", got)
	}
	fetch := permissionByName(t, sessionPermissions, toolpolicy.ToolFetchURL)
	if len(fetch.TargetRules) != 1 || fetch.TargetRules[0].Target != "https://docs.example.test" {
		t.Fatalf("inherited fetch rules = %#v", fetch.TargetRules)
	}

	for index := range workspacePermissions {
		if workspacePermissions[index].ToolName == toolpolicy.ToolReadFile {
			workspacePermissions[index].FilesystemScope = toolpolicy.ScopeComputer
		}
	}
	if _, err := dataStore.ReplaceWorkspaceToolPermissions(ctx, workspace.ID, workspacePermissions); err != nil {
		t.Fatalf("update workspace permissions error = %v", err)
	}
	sessionPermissions, err = dataStore.GetSessionToolPermissions(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("reload session permissions error = %v", err)
	}
	if got := permissionByName(t, sessionPermissions, toolpolicy.ToolReadFile).FilesystemScope; got != toolpolicy.ScopeRepository {
		t.Fatalf("session scope after workspace update = %q", got)
	}

	for index := range sessionPermissions {
		if sessionPermissions[index].ToolName == toolpolicy.ToolReadFile {
			sessionPermissions[index].ConfirmationMode = toolpolicy.ConfirmationAsk
		}
	}
	if _, err := dataStore.ReplaceSessionToolPermissions(ctx, sessionRecord.ID, sessionPermissions); err != nil {
		t.Fatalf("ReplaceSessionToolPermissions() error = %v", err)
	}
	workspacePermissions, err = dataStore.GetWorkspaceToolPermissions(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("reload workspace permissions error = %v", err)
	}
	if got := permissionByName(t, workspacePermissions, toolpolicy.ToolReadFile).ConfirmationMode; got != toolpolicy.ConfirmationAllow {
		t.Fatalf("workspace confirmation after session update = %q", got)
	}
}

func TestCreateWorkspaceRejectsMissingDirectory(t *testing.T) {
	dataStore, err := Open(context.Background(), filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	_, err = dataStore.CreateWorkspace(context.Background(), "Missing", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("CreateWorkspace() error = nil")
	}
}

func permissionByName(t *testing.T, permissions []toolpolicy.Permission, name string) toolpolicy.Permission {
	t.Helper()
	permission, ok := toolpolicy.PermissionFor(permissions, name)
	if !ok {
		t.Fatalf("permission %q is missing from %#v", name, permissions)
	}
	return permission
}
