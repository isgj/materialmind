package workspacetools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"materialmind/internal/toolpolicy"
)

func TestFilesystemAccessKeepsWorkspaceAsRelativeBase(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(repository, "packages", "app")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeRepository)
	if err != nil {
		t.Fatalf("newFilesystemAccess() error = %v", err)
	}

	current, err := access.Resolve(".")
	if err != nil || current.Absolute != workspace {
		t.Fatalf("Resolve(.) = %#v, %v", current, err)
	}
	sibling, err := access.Resolve("../shared/config.json")
	if err != nil || sibling.Absolute != filepath.Join(repository, "packages", "shared", "config.json") {
		t.Fatalf("Resolve(sibling) = %#v, %v", sibling, err)
	}
	if _, err := access.Resolve("../../../outside"); !errors.Is(err, errPathOutsideScope) {
		t.Fatalf("Resolve(outside) error = %v", err)
	}
}

func TestWorkspaceAccessRejectsAbsoluteOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Resolve(filepath.Join(filepath.Dir(workspace), "outside")); !errors.Is(err, errPathOutsideScope) {
		t.Fatalf("Resolve(outside) error = %v", err)
	}
}
