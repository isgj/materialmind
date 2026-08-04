package workspacetools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"materialmind/internal/toolpolicy"
)

func TestPreparedTextFileReadUsesConfiguredBoundaryAndLineRange(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "example.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	line, limit := 2, 1
	prepared, err := PrepareTextFileRead(
		root,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolReadFile,
			ConfirmationMode: toolpolicy.ConfirmationAllow,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		filePath,
		&line,
		&limit,
	)
	if err != nil {
		t.Fatalf("PrepareTextFileRead() error = %v", err)
	}
	result, err := prepared.Execute()
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if prepared.Path() != filePath || result.Content != "two\n" || result.StartLine != 2 || result.EndLine != 2 {
		t.Fatalf("prepared read = %q, %#v", prepared.Path(), result)
	}
	if _, err := PrepareTextFileRead(
		root,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolReadFile,
			ConfirmationMode: toolpolicy.ConfirmationAllow,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		filepath.Join(root, "..", "outside.txt"),
		nil,
		nil,
	); !errors.Is(err, errPathOutsideScope) {
		t.Fatalf("PrepareTextFileRead() boundary error = %v", err)
	}
}

func TestPreparedTextFileWritePreviewsAppliesAndDetectsConflicts(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "example.txt")
	if err := os.WriteFile(filePath, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	permission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolEditFile,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}
	prepared, err := PrepareTextFileWrite(root, permission, filePath, "after\n")
	if err != nil {
		t.Fatalf("PrepareTextFileWrite() error = %v", err)
	}
	preview := prepared.Preview()
	if preview.Operation != "update" || preview.Path != filePath || preview.Noop || preview.Diff == "" {
		t.Fatalf("Preview() = %#v", preview)
	}
	if err := prepared.Apply(); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil || string(content) != "after\n" {
		t.Fatalf("read applied file = %q, %v", content, err)
	}

	conflicting, err := PrepareTextFileWrite(root, permission, filePath, "approved\n")
	if err != nil {
		t.Fatalf("PrepareTextFileWrite() conflict setup error = %v", err)
	}
	if err := os.WriteFile(filePath, []byte("changed elsewhere\n"), 0o600); err != nil {
		t.Fatalf("replace test file: %v", err)
	}
	if err := conflicting.Apply(); !errors.Is(err, errEditConflict) {
		t.Fatalf("Apply() conflict error = %v", err)
	}

	createdPath := filepath.Join(root, "new.txt")
	created, err := PrepareTextFileWrite(root, permission, createdPath, "new\n")
	if err != nil {
		t.Fatalf("PrepareTextFileWrite() create error = %v", err)
	}
	if created.Preview().Operation != "create" {
		t.Fatalf("create Preview() = %#v", created.Preview())
	}
	if err := created.Apply(); err != nil {
		t.Fatalf("create Apply() error = %v", err)
	}
}
