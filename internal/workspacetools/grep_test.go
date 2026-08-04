package workspacetools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"materialmind/internal/toolpolicy"
)

func TestGrepReturnsStructuredFilteredMatches(t *testing.T) {
	ripgrepPath := requireRipgrep(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n// Needle one\n// needle two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("needle ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}

	result, err := grepWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolGrep,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ripgrepPath, nil, GrepArgs{
		Pattern:    "needle",
		Globs:      []string{"*.go"},
		CaseMode:   "insensitive",
		MaxResults: 10,
	})
	if err != nil {
		t.Fatalf("grepWithPolicy() error = %v", err)
	}
	if result.State != "matched" || result.MatchCount != 2 || result.Truncated {
		t.Fatalf("grepWithPolicy() = %#v", result)
	}
	if result.Matches[0].Path != "main.go" || result.Matches[0].Line != 2 || result.Matches[0].Column != 4 || result.Matches[0].Match != "Needle" {
		t.Fatalf("first grep match = %#v", result.Matches[0])
	}
}

func TestGrepStopsAfterBoundedResults(t *testing.T) {
	ripgrepPath := requireRipgrep(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "many.txt"), []byte("match one\nmatch two\nmatch three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}

	result, err := grepWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolGrep,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ripgrepPath, nil, GrepArgs{Pattern: "match", MaxResults: 1})
	if err != nil {
		t.Fatalf("grepWithPolicy() error = %v", err)
	}
	if result.MatchCount != 1 || len(result.Matches) != 1 || !result.Truncated {
		t.Fatalf("grepWithPolicy() = %#v", result)
	}
}

func TestGrepAskValidatesScopeBeforeApproval(t *testing.T) {
	ripgrepPath := requireRipgrep(t)
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	permission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolGrep,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}
	ctx := &fetchTestContext{}

	if _, err := grepWithPolicy(access, permission, ripgrepPath, ctx, GrepArgs{Pattern: "secret", Path: "outside-link"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("grepWithPolicy() error = %v, want outside scope", err)
	}
	if ctx.payload != nil {
		t.Fatalf("out-of-scope grep requested approval: %#v", ctx.payload)
	}

	result, err := grepWithPolicy(access, permission, ripgrepPath, ctx, GrepArgs{Pattern: "safe"})
	if err != nil || result.State != "approval_required" || ctx.payload == nil {
		t.Fatalf("grepWithPolicy() = %#v, %v, payload %#v", result, err, ctx.payload)
	}
}

func TestGrepTreatsPatternAsArgument(t *testing.T) {
	ripgrepPath := requireRipgrep(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "flags.txt"), []byte("--files\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}

	result, err := grepWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolGrep,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ripgrepPath, nil, GrepArgs{Pattern: "--files", FixedStrings: true})
	if err != nil || result.MatchCount != 1 || result.Matches[0].Match != "--files" {
		t.Fatalf("grepWithPolicy() = %#v, %v", result, err)
	}
}

func requireRipgrep(t *testing.T) string {
	t.Helper()
	ripgrepPath, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg is not installed")
	}
	return ripgrepPath
}
