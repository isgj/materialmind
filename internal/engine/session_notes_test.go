package engine

import (
	"context"
	"path/filepath"
	"testing"

	"materialmind/internal/store"
	"materialmind/internal/workspacetools"
)

func TestEngineSessionNotesHandlers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
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
	engine := New(dataStore)

	read, err := engine.readSessionNotes(ctx, sessionRecord.ID)
	if err != nil || read.State != "empty" || read.Revision != 0 || read.Content != "" {
		t.Fatalf("empty read = %#v, %v", read, err)
	}
	updated, err := engine.updateSessionNotes(ctx, sessionRecord.ID, workspacetools.UpdateSessionNotesArgs{
		Content:          "# Decisions\n\n- Preserve the API.\n",
		ExpectedRevision: 0,
	})
	if err != nil || updated.State != "updated" || updated.Revision != 1 || updated.Bytes == 0 || updated.UpdatedAt == "" {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	conflict, err := engine.updateSessionNotes(ctx, sessionRecord.ID, workspacetools.UpdateSessionNotesArgs{
		Content:          "stale",
		ExpectedRevision: 0,
	})
	if err != nil || conflict.State != "conflict" || conflict.Revision != 1 || conflict.Reason == "" {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	read, err = engine.readSessionNotes(ctx, sessionRecord.ID)
	if err != nil || read.State != "read" || read.Revision != 1 || read.Content == "" {
		t.Fatalf("persisted read = %#v, %v", read, err)
	}
}
