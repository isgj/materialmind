package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageSettingsAndExpiredSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	settings, err := dataStore.GetStorageSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.RetentionDays != 0 {
		t.Fatalf("default retention days = %d, want 0", settings.RetentionDays)
	}
	settings, err = dataStore.UpdateStorageSettings(ctx, StorageSettings{RetentionDays: 90})
	if err != nil {
		t.Fatal(err)
	}
	if settings.RetentionDays != 90 {
		t.Fatalf("updated retention days = %d, want 90", settings.RetentionDays)
	}
	if _, err := dataStore.UpdateStorageSettings(ctx, StorageSettings{RetentionDays: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative retention error = %v, want ErrInvalidInput", err)
	}

	workspaceRoot := t.TempDir()
	workspace, err := dataStore.CreateWorkspace(ctx, "Workspace", workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	session, err := dataStore.CreateSession(ctx, workspace.ID, "Old session", nil)
	if err != nil {
		t.Fatal(err)
	}
	old := formatTime(time.Now().UTC().AddDate(0, 0, -100))
	if _, err := dataStore.DB().ExecContext(
		ctx,
		`UPDATE app_sessions SET updated_at = ? WHERE id = ?`,
		old,
		session.ID,
	); err != nil {
		t.Fatal(err)
	}
	expired, err := dataStore.ListExpiredSessions(ctx, time.Now().UTC().AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != session.ID {
		t.Fatalf("expired sessions = %#v", expired)
	}
	if _, err := dataStore.DB().ExecContext(
		ctx,
		`UPDATE app_sessions SET status = 'running' WHERE id = ?`,
		session.ID,
	); err != nil {
		t.Fatal(err)
	}
	expired, err = dataStore.ListExpiredSessions(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("running expired sessions = %#v, want none", expired)
	}
}

func TestBackupCreatesConsistentDatabaseCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	directory := t.TempDir()
	dataStore, err := Open(ctx, filepath.Join(directory, "materialmind.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := dataStore.CreateWorkspace(ctx, "Backed up", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "backup.db")
	if err := dataStore.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("backup permissions = %o, want 600", permissions)
	}

	backup, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	workspaces, err := backup.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != workspace.ID {
		t.Fatalf("backed up workspaces = %#v", workspaces)
	}
	if err := backup.Backup(ctx, destination); err == nil {
		t.Fatal("Backup() overwrote an existing destination")
	}
}
