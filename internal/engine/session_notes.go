package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"materialmind/internal/store"
	"materialmind/internal/workspacetools"
)

func (e *Engine) readSessionNotes(
	ctx context.Context,
	sessionID string,
) (workspacetools.ReadSessionNotesResult, error) {
	notes, err := e.store.GetSessionNotes(ctx, sessionID)
	if err != nil {
		return workspacetools.ReadSessionNotesResult{}, err
	}
	state := "read"
	if notes.Revision == 0 {
		state = "empty"
	}
	return workspacetools.ReadSessionNotesResult{
		State:     state,
		Content:   notes.Content,
		Revision:  notes.Revision,
		UpdatedAt: formatSessionNotesTime(notes.UpdatedAt),
	}, nil
}

func (e *Engine) updateSessionNotes(
	ctx context.Context,
	sessionID string,
	args workspacetools.UpdateSessionNotesArgs,
) (workspacetools.UpdateSessionNotesResult, error) {
	notes, changed, err := e.store.UpdateSessionNotes(
		ctx,
		sessionID,
		args.Content,
		args.ExpectedRevision,
	)
	if errors.Is(err, store.ErrConflict) {
		return workspacetools.UpdateSessionNotesResult{
			State:            "conflict",
			Revision:         notes.Revision,
			ExpectedRevision: args.ExpectedRevision,
			Bytes:            len(args.Content),
			Reason: fmt.Sprintf(
				"Session notes changed after revision %d. Read them again before retrying.",
				args.ExpectedRevision,
			),
		}, nil
	}
	if err != nil {
		return workspacetools.UpdateSessionNotesResult{}, err
	}
	state := "updated"
	if !changed {
		state = "unchanged"
	}
	return workspacetools.UpdateSessionNotesResult{
		State:            state,
		Revision:         notes.Revision,
		ExpectedRevision: args.ExpectedRevision,
		Bytes:            len(notes.Content),
		UpdatedAt:        formatSessionNotesTime(notes.UpdatedAt),
	}, nil
}

func formatSessionNotesTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
