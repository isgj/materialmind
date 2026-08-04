package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"

	"materialmind/internal/store"
)

func TestApplyRetentionDeletesOnlyExpiredIdleSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runEngine := New(dataStore)
	expired, err := runEngine.CreateSession(ctx, workspace.ID, "Expired", nil)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := runEngine.CreateSession(ctx, workspace.ID, "Recent", nil)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -31).Format(time.RFC3339Nano)
	if _, err := dataStore.DB().ExecContext(
		ctx,
		`UPDATE app_sessions SET updated_at = ? WHERE id = ?`,
		old,
		expired.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.UpdateStorageSettings(ctx, store.StorageSettings{RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}

	deleted, err := runEngine.ApplyRetention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted sessions = %d, want 1", deleted)
	}
	if _, err := dataStore.GetSession(ctx, expired.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session error = %v, want ErrNotFound", err)
	}
	if _, err := dataStore.GetSession(ctx, recent.ID); err != nil {
		t.Fatalf("recent session was deleted: %v", err)
	}
	_, err = runEngine.sessionService.Get(ctx, &session.GetRequest{
		AppName: AppName, UserID: UserID, SessionID: expired.ID,
	})
	if err == nil {
		t.Fatal("expired ADK session was retained")
	}
}
