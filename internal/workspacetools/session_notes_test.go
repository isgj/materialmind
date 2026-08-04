package workspacetools

import (
	"context"
	"strings"
	"testing"

	"materialmind/internal/toolpolicy"
)

type sessionNotesTestContext struct {
	fetchTestContext
	sessionID string
}

func (c *sessionNotesTestContext) SessionID() string {
	return c.sessionID
}

func TestReadSessionNotesUsesSessionHandler(t *testing.T) {
	t.Parallel()

	var gotSessionID string
	toolValue, err := newReadSessionNotesTool(func(
		_ context.Context,
		sessionID string,
	) (ReadSessionNotesResult, error) {
		gotSessionID = sessionID
		return ReadSessionNotesResult{
			State:    "read",
			Content:  "# Decisions",
			Revision: 3,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runnable, ok := toolValue.(runnableFunctionTool)
	if !ok {
		t.Fatalf("newReadSessionNotesTool() type = %T", toolValue)
	}
	result, err := runnable.Run(
		&sessionNotesTestContext{sessionID: "session-1"},
		map[string]any{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotSessionID != "session-1" || result["state"] != "read" || result["revision"] != float64(3) {
		t.Fatalf("read result = %#v, session id = %q", result, gotSessionID)
	}
}

func TestSessionNotesAskPolicyRequestsConfirmation(t *testing.T) {
	t.Parallel()

	readPermission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolReadSessionNotes,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
	}
	readContext := &sessionNotesTestContext{sessionID: "session-1"}
	result, err := readSessionNotesWithPolicy(
		readPermission,
		func(context.Context, string) (ReadSessionNotesResult, error) {
			t.Fatal("read handler called before approval")
			return ReadSessionNotesResult{}, nil
		},
		readContext,
	)
	if err != nil || result.State != "approval_required" || readContext.payload == nil || !readContext.actions.SkipSummarization {
		t.Fatalf("read approval = %#v, %v, payload %#v", result, err, readContext.payload)
	}

	updatePermission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolUpdateSessionNotes,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
	}
	updateContext := &sessionNotesTestContext{sessionID: "session-1"}
	update, err := updateSessionNotesWithPolicy(
		updatePermission,
		func(context.Context, string, UpdateSessionNotesArgs) (UpdateSessionNotesResult, error) {
			t.Fatal("update handler called before approval")
			return UpdateSessionNotesResult{}, nil
		},
		updateContext,
		UpdateSessionNotesArgs{Content: "notes", ExpectedRevision: 2},
	)
	if err != nil || update.State != "approval_required" || update.ExpectedRevision != 2 || update.Bytes != 5 || updateContext.payload == nil {
		t.Fatalf("update approval = %#v, %v, payload %#v", update, err, updateContext.payload)
	}
}

func TestUpdateSessionNotesValidatesAndDelegates(t *testing.T) {
	t.Parallel()

	var gotSessionID string
	var gotArgs UpdateSessionNotesArgs
	handler := func(
		_ context.Context,
		sessionID string,
		args UpdateSessionNotesArgs,
	) (UpdateSessionNotesResult, error) {
		gotSessionID = sessionID
		gotArgs = args
		return UpdateSessionNotesResult{
			State:    "updated",
			Revision: 2,
			Bytes:    len(args.Content),
		}, nil
	}
	ctx := &sessionNotesTestContext{sessionID: "session-2"}
	result, err := updateSessionNotesWithPolicy(
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolUpdateSessionNotes,
			ConfirmationMode: toolpolicy.ConfirmationAllow,
		},
		handler,
		ctx,
		UpdateSessionNotesArgs{Content: "# Notes", ExpectedRevision: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "updated" || gotSessionID != "session-2" || gotArgs.ExpectedRevision != 1 || gotArgs.Content != "# Notes" {
		t.Fatalf("update result = %#v, session id = %q, args = %#v", result, gotSessionID, gotArgs)
	}

	_, err = updateSessionNotesWithPolicy(
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolUpdateSessionNotes,
			ConfirmationMode: toolpolicy.ConfirmationAllow,
		},
		handler,
		ctx,
		UpdateSessionNotesArgs{Content: strings.Repeat("x", maxSessionNotesBytes+1)},
	)
	if err == nil || !strings.Contains(err.Error(), "16384") {
		t.Fatalf("oversized update error = %v", err)
	}
}
