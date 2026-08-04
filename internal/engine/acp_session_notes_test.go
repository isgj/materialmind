package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/acpinternal"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
	"materialmind/internal/workspacetools"
)

func TestACPInternalSessionNotesUseScopedTokenAndCanonicalTimeline(t *testing.T) {
	runEngine, dataStore, handler, token := newACPInternalNotesTest(t, toolpolicy.DefaultPermissions())
	if _, err := runEngine.CallACPInternalTool(
		t.Context(), "wrong-token", acpinternal.ToolReadSessionNotes, json.RawMessage(`{}`),
	); !errors.Is(err, ErrACPInternalUnauthorized) {
		t.Fatalf("CallACPInternalTool() unauthorized error = %v", err)
	}

	readValue, err := runEngine.CallACPInternalTool(
		t.Context(), token, acpinternal.ToolReadSessionNotes, json.RawMessage(`{}`),
	)
	read, ok := readValue.(workspacetools.ReadSessionNotesResult)
	if err != nil || !ok || read.State != "empty" || read.Revision != 0 {
		t.Fatalf("read notes = %#v, %v", readValue, err)
	}
	updateValue, err := runEngine.CallACPInternalTool(
		t.Context(),
		token,
		acpinternal.ToolUpdateSessionNotes,
		json.RawMessage(`{"content":"# Decisions\n\n- Keep the API.\n","expectedRevision":0}`),
	)
	updated, ok := updateValue.(workspacetools.UpdateSessionNotesResult)
	if err != nil || !ok || updated.State != "updated" || updated.Revision != 1 {
		t.Fatalf("update notes = %#v, %v", updateValue, err)
	}
	notes, err := dataStore.GetSessionNotes(t.Context(), handler.session.ID)
	if err != nil || notes.Revision != 1 || notes.Content == "" {
		t.Fatalf("stored notes = %#v, %v", notes, err)
	}
	transcript, err := dataStore.ListACPTranscript(t.Context(), handler.session.ID)
	if err != nil {
		t.Fatalf("ListACPTranscript() error = %v", err)
	}
	if len(transcript) != 5 || transcript[1].ToolName != toolpolicy.ToolReadSessionNotes ||
		transcript[3].ToolName != toolpolicy.ToolUpdateSessionNotes {
		t.Fatalf("canonical notes transcript = %#v", transcript)
	}
}

func TestACPInternalSessionNotesWaitForConfiguredApproval(t *testing.T) {
	permissions := toolpolicy.DefaultPermissions()
	for index := range permissions {
		if permissions[index].ToolName == toolpolicy.ToolUpdateSessionNotes {
			permissions[index].ConfirmationMode = toolpolicy.ConfirmationAsk
		}
	}
	runEngine, _, handler, token := newACPInternalNotesTest(t, permissions)
	type callResult struct {
		value any
		err   error
	}
	result := make(chan callResult, 1)
	go func() {
		value, err := runEngine.CallACPInternalTool(
			context.Background(),
			token,
			acpinternal.ToolUpdateSessionNotes,
			json.RawMessage(`{"content":"approved","expectedRevision":0}`),
		)
		result <- callResult{value: value, err: err}
	}()

	var approval ToolApprovalRequest
	deadline := time.After(2 * time.Second)
	for approval.ID == "" {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session notes approval")
		default:
			runEngine.mu.Lock()
			for _, pending := range runEngine.active[handler.session.ID].pendingApprovals {
				approval = pending.request
				break
			}
			runEngine.mu.Unlock()
			if approval.ID == "" {
				time.Sleep(time.Millisecond)
			}
		}
	}
	if approval.ToolName != toolpolicy.ToolUpdateSessionNotes ||
		approval.Payload["kind"] != "session_notes" || approval.Payload["operation"] != "update" {
		t.Fatalf("notes approval = %#v", approval)
	}
	if _, err := runEngine.ResolveToolApproval(
		t.Context(), handler.run.ID, approval.ID, true, "", "",
	); err != nil {
		t.Fatalf("ResolveToolApproval() error = %v", err)
	}
	call := <-result
	updated, ok := call.value.(workspacetools.UpdateSessionNotesResult)
	if call.err != nil || !ok || updated.State != "updated" {
		t.Fatalf("approved update = %#v, %v", call.value, call.err)
	}
}

func TestACPInternalMCPNotificationsAreSuppressed(t *testing.T) {
	_, dataStore, handler, _ := newACPInternalNotesTest(t, toolpolicy.DefaultPermissions())
	if err := handler.SessionUpdate(t.Context(), acp.SessionNotification{
		Update: acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
			SessionUpdate: "tool_call",
			ToolCallId:    "agent-mcp-call",
			Title:         "mcp." + acpinternal.ServerName + "." + acpinternal.ToolReadSessionNotes,
			Kind:          acp.ToolKindExecute,
			Status:        acp.ToolCallStatusInProgress,
			RawInput: map[string]any{
				"server":    acpinternal.ServerName,
				"tool":      acpinternal.ToolReadSessionNotes,
				"arguments": map[string]any{},
			},
		}},
	}); err != nil {
		t.Fatalf("SessionUpdate() error = %v", err)
	}
	transcript, err := dataStore.ListACPTranscript(t.Context(), handler.session.ID)
	if err != nil {
		t.Fatalf("ListACPTranscript() error = %v", err)
	}
	if len(transcript) != 1 || transcript[0].Kind != "message" {
		t.Fatalf("internal ACP notification created duplicate transcript items: %#v", transcript)
	}
}

func TestACPInternalMCPAgentApprovalDefersToBackendPermission(t *testing.T) {
	runEngine, dataStore, handler, _ := newACPInternalNotesTest(t, toolpolicy.DefaultPermissions())
	response, err := handler.RequestPermission(t.Context(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: "agent-mcp-call",
			Title:      acp.Ptr("mcp." + acpinternal.ServerName + "." + acpinternal.ToolReadSessionNotes),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			RawInput: map[string]any{
				"server":    acpinternal.ServerName,
				"tool":      acpinternal.ToolReadSessionNotes,
				"arguments": map[string]any{},
			},
		},
		Options: []acp.PermissionOption{
			{
				OptionId: "allow-once",
				Name:     "Allow once",
				Kind:     acp.PermissionOptionKindAllowOnce,
			},
			{
				OptionId: "reject-once",
				Name:     "Reject",
				Kind:     acp.PermissionOptionKindRejectOnce,
			},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission() error = %v", err)
	}
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow-once" {
		t.Fatalf("RequestPermission() = %#v", response)
	}
	runEngine.mu.Lock()
	pending := len(runEngine.active[handler.session.ID].pendingApprovals)
	runEngine.mu.Unlock()
	if pending != 0 {
		t.Fatalf("ACP-level approval was registered, pending = %d", pending)
	}
	transcript, err := dataStore.ListACPTranscript(t.Context(), handler.session.ID)
	if err != nil {
		t.Fatalf("ListACPTranscript() error = %v", err)
	}
	if len(transcript) != 1 || transcript[0].Kind != "message" {
		t.Fatalf("ACP-level approval created duplicate transcript items: %#v", transcript)
	}
}

func newACPInternalNotesTest(
	t *testing.T,
	permissions []toolpolicy.Permission,
) (*Engine, *store.Store, *acpRunHandler, string) {
	t.Helper()
	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataStore.Close() })
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dataStore.CreateACPAgent(ctx, "ACP", "missing-acp-test-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := dataStore.CreateACPSession(ctx, workspace.ID, "Notes", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := dataStore.CreateACPRun(ctx, sessionRecord.ID, "Use notes")
	if err != nil {
		t.Fatal(err)
	}
	runEngine := New(dataStore)
	runEngine.acpInternalMCPEnabled = true
	runEngine.hub.Create(run.ID)
	handler := &acpRunHandler{
		engine:           runEngine,
		ctx:              ctx,
		run:              run,
		session:          sessionRecord,
		workspaceRoot:    workspace.RootPath,
		permissions:      permissions,
		segments:         make(map[string]string),
		tools:            make(map[string]*acpToolState),
		terminalTools:    make(map[string]string),
		pendingTerminals: make(map[string]map[string]string),
	}
	runEngine.active[sessionRecord.ID] = &activeRun{
		runID:                  run.ID,
		pendingApprovals:       make(map[string]*pendingToolApproval),
		pendingUserInputs:      make(map[string]*pendingUserInput),
		pendingMCPElicitations: make(map[string]*pendingMCPElicitation),
		acpHandler:             handler,
	}
	return runEngine, dataStore, handler, runEngine.acpInternalMCPToken(sessionRecord.ID)
}
