package workspacetools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

type editTestContext struct {
	agent.ContextMock
	actions      session.EventActions
	confirmation *toolconfirmation.ToolConfirmation
	hint         string
	payload      any
}

func (c *editTestContext) Actions() *session.EventActions {
	return &c.actions
}

func (c *editTestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return c.confirmation
}

func (c *editTestContext) RequestConfirmation(hint string, payload any) error {
	c.hint = hint
	c.payload = payload
	return nil
}

func TestEditFileRequestsApprovalWithoutWriting(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	before := "package main\n\nconst greeting = \"hello\"\n"
	if err := os.WriteFile(filePath, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx := &editTestContext{}
	result, err := editFile(root, ctx, EditFileArgs{
		Path:  "main.go",
		Edits: []TextReplacement{{OldText: "hello", NewText: "hello, world"}},
	})
	if err != nil {
		t.Fatalf("editFile() error = %v", err)
	}
	if result.State != "approval_required" || result.Path != "main.go" ||
		!strings.Contains(result.Diff, "-const greeting = \"hello\"") ||
		!strings.Contains(result.Diff, "+const greeting = \"hello, world\"") {
		t.Fatalf("editFile() = %#v", result)
	}
	if !strings.Contains(ctx.hint, "main.go") || ctx.payload == nil || !ctx.actions.SkipSummarization {
		t.Fatalf("confirmation hint = %q, payload = %#v, actions = %#v", ctx.hint, ctx.payload, ctx.actions)
	}
	assertFileContent(t, filePath, before)
}

func TestEditFileAllowModeAppliesWithoutApproval(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(root, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &editTestContext{}
	result, err := editFileWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolEditFile,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ctx, EditFileArgs{Path: "main.go", Edits: []TextReplacement{{
		OldText: "package main",
		NewText: "package example",
	}}})
	if err != nil || result.State != "applied" || ctx.payload != nil {
		t.Fatalf("editFileWithPolicy() = %#v, %v, payload %#v", result, err, ctx.payload)
	}
	content, err := os.ReadFile(filePath)
	if err != nil || string(content) != "package example\n" {
		t.Fatalf("edited content = %q, %v", content, err)
	}
}

func TestEditFileToolSchemaSupportsBatchAndLegacyInputs(t *testing.T) {
	editTool, err := newEditFileTool(t.TempDir())
	if err != nil {
		t.Fatalf("newEditFileTool() error = %v", err)
	}
	runnable, ok := editTool.(runnableFunctionTool)
	if !ok {
		t.Fatalf("edit tool type = %T", editTool)
	}
	encoded, err := json.Marshal(runnable.Declaration().ParametersJsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["changes"] == nil ||
		schema.Properties["path"] == nil ||
		schema.Properties["edits"] == nil {
		t.Fatalf("edit tool parameters = %s", encoded)
	}
	for _, optional := range []string{"changes", "path", "edits"} {
		if slices.Contains(schema.Required, optional) {
			t.Fatalf("edit tool unexpectedly requires %q: %#v", optional, schema.Required)
		}
	}
}

func TestEditFileAppliesTheApprovedProposalAtomically(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	before := "package main\n\nconst greeting = \"hello\"\n"
	if err := os.WriteFile(filePath, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	args := EditFileArgs{
		Path:  "main.go",
		Edits: []TextReplacement{{OldText: "hello", NewText: "hello, world"}},
	}
	requestContext := &editTestContext{}
	if _, err := editFile(root, requestContext, args); err != nil {
		t.Fatalf("request editFile() error = %v", err)
	}
	approvalContext := &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   requestContext.payload,
	}}
	result, err := editFile(root, approvalContext, args)
	if err != nil {
		t.Fatalf("approved editFile() error = %v", err)
	}
	if result.State != "applied" || result.EditCount != 1 {
		t.Fatalf("approved editFile() = %#v", result)
	}
	assertFileContent(t, filePath, strings.Replace(before, "hello", "hello, world", 1))
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("edited file mode = %o, want 640", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(root, ".main.go.materialmind-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %#v", matches)
	}
}

func TestEditFileRequestsOneApprovalForCreateUpdateAndDelete(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	obsoletePath := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(mainPath, []byte("const port = 8080\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obsoletePath, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := EditFileArgs{Changes: []FileChange{
		{Operation: fileOperationCreate, Path: "created.txt", Content: "new file\n"},
		{
			Operation: fileOperationUpdate,
			Path:      "main.go",
			Edits:     []TextReplacement{{OldText: "8080", NewText: "9000"}},
		},
		{Operation: fileOperationDelete, Path: "obsolete.txt"},
	}}
	ctx := &editTestContext{}
	result, err := editFile(root, ctx, args)
	if err != nil {
		t.Fatalf("editFile() error = %v", err)
	}
	if result.State != "approval_required" || result.ChangeCount != 3 || result.EditCount != 1 {
		t.Fatalf("editFile() = %#v", result)
	}
	if !strings.Contains(ctx.hint, "3 files") {
		t.Fatalf("confirmation hint = %q", ctx.hint)
	}
	payload, ok := ctx.payload.(editConfirmationPayload)
	if !ok || payload.Kind != "file_patch" || len(payload.Files) != 3 {
		t.Fatalf("confirmation payload = %#v", ctx.payload)
	}
	if payload.Files[0].Operation != fileOperationCreate ||
		!strings.Contains(payload.Files[0].Diff, "--- /dev/null") ||
		payload.Files[1].Operation != fileOperationUpdate ||
		payload.Files[2].Operation != fileOperationDelete ||
		!strings.Contains(payload.Files[2].Diff, "+++ /dev/null") {
		t.Fatalf("confirmation files = %#v", payload.Files)
	}
	assertFileMissing(t, filepath.Join(root, "created.txt"))
	assertFileContent(t, mainPath, "const port = 8080\n")
	assertFileContent(t, obsoletePath, "remove me\n")
}

func TestEditFileAppliesApprovedBatch(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	obsoletePath := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(mainPath, []byte("const port = 8080\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obsoletePath, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := EditFileArgs{Changes: []FileChange{
		{Operation: fileOperationCreate, Path: "created.txt", Content: "new file\n"},
		{
			Operation: fileOperationUpdate,
			Path:      "main.go",
			Edits:     []TextReplacement{{OldText: "8080", NewText: "9000"}},
		},
		{Operation: fileOperationDelete, Path: "obsolete.txt"},
	}}
	requestContext := &editTestContext{}
	if _, err := editFile(root, requestContext, args); err != nil {
		t.Fatalf("request editFile() error = %v", err)
	}
	result, err := editFile(root, &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   requestContext.payload,
	}}, args)
	if err != nil {
		t.Fatalf("approved editFile() error = %v", err)
	}
	if result.State != "applied" || result.ChangeCount != 3 || len(result.Paths) != 3 {
		t.Fatalf("approved editFile() = %#v", result)
	}
	assertFileContent(t, filepath.Join(root, "created.txt"), "new file\n")
	assertFileContent(t, mainPath, "const port = 9000\n")
	assertFileMissing(t, obsoletePath)
	info, err := os.Stat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("updated file mode = %o, want 640", info.Mode().Perm())
	}
	artifacts, err := filepath.Glob(filepath.Join(root, ".*.materialmind-*.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("temporary files remain: %#v", artifacts)
	}
}

func TestEditFileRejectsStaleBatchBeforeChangingAnyFile(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.txt")
	secondPath := filepath.Join(root, "second.txt")
	if err := os.WriteFile(firstPath, []byte("first old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := EditFileArgs{Changes: []FileChange{
		{
			Operation: fileOperationUpdate,
			Path:      "first.txt",
			Edits:     []TextReplacement{{OldText: "old", NewText: "new"}},
		},
		{Operation: fileOperationDelete, Path: "second.txt"},
		{Operation: fileOperationCreate, Path: "third.txt", Content: "third\n"},
	}}
	requestContext := &editTestContext{}
	if _, err := editFile(root, requestContext, args); err != nil {
		t.Fatalf("request editFile() error = %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("changed elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := editFile(root, &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   requestContext.payload,
	}}, args)
	if err != nil {
		t.Fatalf("approved editFile() error = %v", err)
	}
	if result.State != "conflict" || !result.Conflict {
		t.Fatalf("approved editFile() = %#v", result)
	}
	assertFileContent(t, firstPath, "first old\n")
	assertFileContent(t, secondPath, "changed elsewhere\n")
	assertFileMissing(t, filepath.Join(root, "third.txt"))
}

func TestNormalizeFileChangesRejectsDuplicatePaths(t *testing.T) {
	_, err := normalizeFileChanges(EditFileArgs{Changes: []FileChange{
		{Operation: fileOperationCreate, Path: "nested/../same.txt", Content: "first"},
		{Operation: fileOperationDelete, Path: "same.txt"},
	}})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("normalizeFileChanges() error = %v", err)
	}
}

func TestEditFileCanCreateAndDeleteEmptyFiles(t *testing.T) {
	root := t.TempDir()
	emptyPath := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	args := EditFileArgs{Changes: []FileChange{
		{Operation: fileOperationDelete, Path: "empty.txt"},
		{Operation: fileOperationCreate, Path: "replacement.txt", Content: ""},
	}}
	requestContext := &editTestContext{}
	if _, err := editFile(root, requestContext, args); err != nil {
		t.Fatalf("request editFile() error = %v", err)
	}
	payload := requestContext.payload.(editConfirmationPayload)
	if !strings.Contains(payload.Files[0].Diff, "+++ /dev/null") ||
		!strings.Contains(payload.Files[1].Diff, "--- /dev/null") {
		t.Fatalf("empty-file diffs = %#v", payload.Files)
	}
	if _, err := editFile(root, &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   requestContext.payload,
	}}, args); err != nil {
		t.Fatalf("approved editFile() error = %v", err)
	}
	assertFileMissing(t, emptyPath)
	assertFileContent(t, filepath.Join(root, "replacement.txt"), "")
}

func TestRollbackPatchSurfacesRestoreFailure(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = rollbackPatch(root, []patchTransactionEntry{{
		file:          filePatchProposal{path: "main.go"},
		backupPath:    ".missing-backup",
		originalMoved: true,
	}}, errEditConflict)
	if !errors.Is(err, errPatchRollback) || errors.Is(err, errEditConflict) {
		t.Fatalf("rollbackPatch() error = %v", err)
	}
}

func TestEditFileReturnsDenialReasonWithoutReadingOrWriting(t *testing.T) {
	root := t.TempDir()
	ctx := &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: false,
		Payload:   map[string]any{"reason": "Keep this file unchanged."},
	}}
	result, err := editFile(root, ctx, EditFileArgs{
		Path:  "missing.go",
		Edits: []TextReplacement{{OldText: "old", NewText: "new"}},
	})
	if err != nil {
		t.Fatalf("editFile() error = %v", err)
	}
	if result.State != "denied" || result.Reason != "Keep this file unchanged." {
		t.Fatalf("editFile() = %#v", result)
	}
}

func TestEditFileRejectsAStaleApprovedProposal(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	args := EditFileArgs{
		Path:  "main.go",
		Edits: []TextReplacement{{OldText: "hello", NewText: "hello, world"}},
	}
	if err := os.WriteFile(filePath, []byte("const greeting = \"hello\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestContext := &editTestContext{}
	if _, err := editFile(root, requestContext, args); err != nil {
		t.Fatalf("request editFile() error = %v", err)
	}
	changed := "const greeting = \"changed elsewhere\"\n"
	if err := os.WriteFile(filePath, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := editFile(root, &editTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   requestContext.payload,
	}}, args)
	if err != nil {
		t.Fatalf("approved editFile() error = %v", err)
	}
	if result.State != "conflict" || !result.Conflict {
		t.Fatalf("approved editFile() = %#v", result)
	}
	assertFileContent(t, filePath, changed)
}

func TestBuildEditProposalRequiresAnExactUniqueMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("same\nsame\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := buildEditProposal(root, "notes.txt", []TextReplacement{{OldText: "same", NewText: "changed"}})
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("buildEditProposal() error = %v", err)
	}
}

func TestEditFileCannotEscapeTheWorkspaceOrFollowSymlinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	edits := []TextReplacement{{OldText: "secret", NewText: "exposed"}}
	if _, err := buildEditProposal(root, "../outside.txt", edits); err == nil {
		t.Fatal("buildEditProposal() traversal error = nil")
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := buildEditProposal(root, "outside-link", edits); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("buildEditProposal() symlink error = %v", err)
	}
	assertFileContent(t, outside, "secret")
}

func assertFileContent(t *testing.T, filePath, expected string) {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("file content = %q, want %q", content, expected)
	}
}

func assertFileMissing(t *testing.T, filePath string) {
	t.Helper()
	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v, want not exist", filePath, err)
	}
}
