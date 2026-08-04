package workspacetools

import (
	"context"
	"fmt"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

const maxSessionNotesBytes = 16 * 1024

type ReadSessionNotesArgs struct{}

type ReadSessionNotesResult struct {
	State     string `json:"state"`
	Content   string `json:"content"`
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type UpdateSessionNotesArgs struct {
	Content          string `json:"content" jsonschema:"Complete replacement Markdown for the session notes. May be empty to clear the notes. Must not exceed 16384 UTF-8 bytes."`
	ExpectedRevision int64  `json:"expectedRevision" jsonschema:"Revision returned by read_session_notes. The update is rejected if the notes changed after that read."`
}

type UpdateSessionNotesResult struct {
	State            string `json:"state"`
	Revision         int64  `json:"revision"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
	Bytes            int    `json:"bytes"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type ReadSessionNotesHandler func(
	context.Context,
	string,
) (ReadSessionNotesResult, error)

type UpdateSessionNotesHandler func(
	context.Context,
	string,
	UpdateSessionNotesArgs,
) (UpdateSessionNotesResult, error)

type SessionNotesHandlers struct {
	Read   ReadSessionNotesHandler
	Update UpdateSessionNotesHandler
}

type sessionNotesConfirmationPayload struct {
	Kind             string `json:"kind"`
	Operation        string `json:"operation"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
}

func newReadSessionNotesTool(
	handler ReadSessionNotesHandler,
	provided ...toolpolicy.Permission,
) (tool.Tool, error) {
	permission := configuredPermission(toolpolicy.ToolReadSessionNotes, provided)
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name: toolpolicy.ToolReadSessionNotes,
			Description: "Read the concise durable Markdown notes maintained explicitly for this session. " +
				"Notes are not conversation history and are never loaded automatically. Read them only when " +
				"durable decisions, user constraints, important discoveries, or unresolved questions are needed. " +
				"Do not use session notes as a substitute for the current conversation or working context.",
		},
		func(ctx agent.Context, _ ReadSessionNotesArgs) (ReadSessionNotesResult, error) {
			return readSessionNotesWithPolicy(permission, handler, ctx)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, sessionNotesDeniedResult("read"))
}

func newUpdateSessionNotesTool(
	handler UpdateSessionNotesHandler,
	provided ...toolpolicy.Permission,
) (tool.Tool, error) {
	permission := configuredPermission(toolpolicy.ToolUpdateSessionNotes, provided)
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name: toolpolicy.ToolUpdateSessionNotes,
			Description: "Replace the complete durable Markdown notes for this session. Always call " +
				"read_session_notes first and pass its revision. Keep only concise, durable decisions, user " +
				"constraints, important discoveries, and unresolved questions; revise or remove stale entries. " +
				"Never store private reasoning, routine progress, plans, logs, file contents, or credentials. " +
				"Session notes are separate from conversation context and are not updated during context compaction.",
		},
		func(ctx agent.Context, args UpdateSessionNotesArgs) (UpdateSessionNotesResult, error) {
			return updateSessionNotesWithPolicy(permission, handler, ctx, args)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, sessionNotesDeniedResult("update"))
}

func readSessionNotesWithPolicy(
	permission toolpolicy.Permission,
	handler ReadSessionNotesHandler,
	ctx agent.Context,
) (ReadSessionNotesResult, error) {
	if ctx == nil {
		return ReadSessionNotesResult{}, fmt.Errorf("agent context is required to read session notes")
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk && ctx.ToolConfirmation() == nil {
		if err := requestSessionNotesConfirmation(ctx, "read", 0); err != nil {
			return ReadSessionNotesResult{}, err
		}
		return ReadSessionNotesResult{State: "approval_required"}, nil
	}
	if confirmation := ctx.ToolConfirmation(); confirmation != nil && !confirmation.Confirmed {
		return ReadSessionNotesResult{
			State:  "denied",
			Reason: approvalReason(confirmation),
		}, nil
	}
	if handler == nil {
		return ReadSessionNotesResult{}, fmt.Errorf("read_session_notes is unavailable")
	}
	return handler(ctx, ctx.SessionID())
}

func updateSessionNotesWithPolicy(
	permission toolpolicy.Permission,
	handler UpdateSessionNotesHandler,
	ctx agent.Context,
	args UpdateSessionNotesArgs,
) (UpdateSessionNotesResult, error) {
	if args.ExpectedRevision < 0 {
		return UpdateSessionNotesResult{}, fmt.Errorf("expectedRevision must not be negative")
	}
	if !utf8.ValidString(args.Content) {
		return UpdateSessionNotesResult{}, fmt.Errorf("content must be valid UTF-8")
	}
	if len(args.Content) > maxSessionNotesBytes {
		return UpdateSessionNotesResult{}, fmt.Errorf(
			"content must not exceed %d UTF-8 bytes",
			maxSessionNotesBytes,
		)
	}
	if ctx == nil {
		return UpdateSessionNotesResult{}, fmt.Errorf("agent context is required to update session notes")
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk && ctx.ToolConfirmation() == nil {
		if err := requestSessionNotesConfirmation(ctx, "update", args.ExpectedRevision); err != nil {
			return UpdateSessionNotesResult{}, err
		}
		return UpdateSessionNotesResult{
			State:            "approval_required",
			ExpectedRevision: args.ExpectedRevision,
			Bytes:            len(args.Content),
		}, nil
	}
	if confirmation := ctx.ToolConfirmation(); confirmation != nil && !confirmation.Confirmed {
		return UpdateSessionNotesResult{
			State:            "denied",
			ExpectedRevision: args.ExpectedRevision,
			Bytes:            len(args.Content),
			Reason:           approvalReason(confirmation),
		}, nil
	}
	if handler == nil {
		return UpdateSessionNotesResult{}, fmt.Errorf("update_session_notes is unavailable")
	}
	return handler(ctx, ctx.SessionID(), args)
}

func requestSessionNotesConfirmation(
	ctx agent.Context,
	operation string,
	expectedRevision int64,
) error {
	verb := "read"
	if operation == "update" {
		verb = "replace"
	}
	if err := ctx.RequestConfirmation(
		fmt.Sprintf("Allow the agent to %s the session notes?", verb),
		sessionNotesConfirmationPayload{
			Kind:             "session_notes",
			Operation:        operation,
			ExpectedRevision: expectedRevision,
		},
	); err != nil {
		return fmt.Errorf("request session notes approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func sessionNotesDeniedResult(operation string) deniedResultFunc {
	return func(
		input map[string]any,
		confirmation *toolconfirmation.ToolConfirmation,
	) (map[string]any, error) {
		result := map[string]any{
			"state":  "denied",
			"reason": approvalReason(confirmation),
		}
		if operation == "update" {
			result["expectedRevision"] = input["expectedRevision"]
			if content, ok := input["content"].(string); ok {
				result["bytes"] = len(content)
			}
		}
		return result, nil
	}
}
