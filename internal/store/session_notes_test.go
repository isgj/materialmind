package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionNotesRevisionLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Notes", nil)
	if err != nil {
		t.Fatal(err)
	}

	notes, err := dataStore.GetSessionNotes(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if notes.SessionID != sessionRecord.ID || notes.Content != "" || notes.Revision != 0 || !notes.UpdatedAt.IsZero() {
		t.Fatalf("empty notes = %#v", notes)
	}

	notes, changed, err := dataStore.UpdateSessionNotes(
		ctx,
		sessionRecord.ID,
		"# Decisions\n\n- Keep the API stable.\n",
		0,
	)
	if err != nil || !changed {
		t.Fatalf("first UpdateSessionNotes() = %#v, %v, changed %t", notes, err, changed)
	}
	if notes.Revision != 1 || notes.UpdatedAt.IsZero() {
		t.Fatalf("first notes = %#v", notes)
	}

	notes, changed, err = dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, notes.Content, 1)
	if err != nil || changed || notes.Revision != 1 {
		t.Fatalf("unchanged UpdateSessionNotes() = %#v, %v, changed %t", notes, err, changed)
	}

	current, changed, err := dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, "stale", 0)
	if !errors.Is(err, ErrConflict) || changed || current.Revision != 1 || current.Content != notes.Content {
		t.Fatalf("stale UpdateSessionNotes() = %#v, %v, changed %t", current, err, changed)
	}

	notes, changed, err = dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, "", 1)
	if err != nil || !changed || notes.Revision != 2 || notes.Content != "" {
		t.Fatalf("clear UpdateSessionNotes() = %#v, %v, changed %t", notes, err, changed)
	}
}

func TestSessionNotesValidationAndSessionCascade(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Notes", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, "notes", -1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative revision error = %v", err)
	}
	if _, _, err := dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, strings.Repeat("x", 16*1024+1), 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized notes error = %v", err)
	}
	if _, _, err := dataStore.UpdateSessionNotes(ctx, "missing", "notes", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	if _, _, err := dataStore.UpdateSessionNotes(ctx, sessionRecord.ID, "notes", 0); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.DeleteSession(ctx, sessionRecord.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dataStore.DB().QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM session_notes WHERE session_id = ?`,
		sessionRecord.ID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("session notes rows after delete = %d, want 0", count)
	}
}
