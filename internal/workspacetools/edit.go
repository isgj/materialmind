package workspacetools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
	"github.com/google/uuid"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

const (
	maxEditFileBytes     = 256 * 1024
	maxPatchPayloadBytes = 1024 * 1024
	maxFileEdits         = 32
	maxPatchFiles        = 32

	fileOperationCreate = "create"
	fileOperationUpdate = "update"
	fileOperationDelete = "delete"
)

var (
	errEditConflict  = errors.New("file changed after edit approval")
	errPatchRollback = errors.New("file patch rollback failed")
)

type TextReplacement struct {
	OldText string `json:"oldText" jsonschema:"Exact existing text to replace. It must occur exactly once in the file."`
	NewText string `json:"newText" jsonschema:"Replacement text. Use an empty string to delete the matched text."`
}

type FileChange struct {
	Operation string            `json:"operation" jsonschema:"File operation: create, update, or delete."`
	Path      string            `json:"path" jsonschema:"File path. Relative paths start at the workspace root."`
	Content   string            `json:"content,omitempty" jsonschema:"Complete UTF-8 file content. Required for create; omit for update and delete."`
	Edits     []TextReplacement `json:"edits,omitempty" jsonschema:"Ordered exact text replacements. Required for update; omit for create and delete."`
}

type EditFileArgs struct {
	Path    string            `json:"path,omitempty" jsonschema:"Legacy single-file update path. Relative paths start at the workspace root. Use changes for new calls."`
	Edits   []TextReplacement `json:"edits,omitempty" jsonschema:"Legacy ordered replacements for path. Use changes for new calls."`
	Changes []FileChange      `json:"changes,omitempty" jsonschema:"One or more create, update, or delete operations applied together after one approval."`
}

type EditFileResult struct {
	State       string   `json:"state"`
	Path        string   `json:"path,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Diff        string   `json:"diff,omitempty"`
	ChangeCount int      `json:"changeCount,omitempty"`
	EditCount   int      `json:"editCount,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Conflict    bool     `json:"conflict,omitempty"`
}

type editConfirmationPayload struct {
	Kind  string                 `json:"kind"`
	Diff  string                 `json:"diff"`
	Files []fileApprovalProposal `json:"files"`
}

type fileApprovalProposal struct {
	Operation    string `json:"operation"`
	Path         string `json:"path"`
	Diff         string `json:"diff"`
	BaseSHA256   string `json:"baseSha256,omitempty"`
	BaseMode     uint32 `json:"baseMode,omitempty"`
	ResultSHA256 string `json:"resultSha256,omitempty"`
}

type editSnapshot struct {
	exists  bool
	content []byte
	mode    os.FileMode
	hash    string
}

type filePatchProposal struct {
	operation   string
	path        string
	displayPath string
	before      editSnapshot
	after       []byte
	diff        string
	afterHash   string
	editCount   int
}

type patchProposal struct {
	rootPath  string
	files     []filePatchProposal
	diff      string
	editCount int
}

type patchTransactionEntry struct {
	file          filePatchProposal
	stagedPath    string
	backupPath    string
	originalMoved bool
	installed     bool
}

func newEditFileTool(rootPath string, provided ...toolpolicy.Permission) (tool.Tool, error) {
	permission := configuredPermission(toolpolicy.ToolEditFile, provided)
	access, err := newFilesystemAccess(rootPath, permission.FilesystemScope)
	if err != nil {
		return nil, err
	}
	confirmationDescription := "Calls follow the configured file-edit confirmation policy."
	if permission.ConfirmationMode == toolpolicy.ConfirmationAllow {
		confirmationDescription = "Valid patches may be applied without user confirmation."
	}
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name:        toolpolicy.ToolEditFile,
			Description: "Apply one transactional batch of file changes. Use changes to create files with full content, update files with exact text replacements, or delete files. Multiple changes are processed together. " + access.Description() + " " + confirmationDescription,
		},
		func(ctx agent.Context, args EditFileArgs) (EditFileResult, error) {
			return editFileWithPolicy(access, permission, ctx, args)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, editFileDeniedResult(access))
}

func editFile(rootPath string, ctx agent.Context, args EditFileArgs) (EditFileResult, error) {
	permission := configuredPermission(toolpolicy.ToolEditFile, nil)
	access, err := newFilesystemAccess(rootPath, permission.FilesystemScope)
	if err != nil {
		return EditFileResult{}, err
	}
	return editFileWithPolicy(access, permission, ctx, args)
}

func editFileWithPolicy(access filesystemAccess, permission toolpolicy.Permission, ctx agent.Context, args EditFileArgs) (EditFileResult, error) {
	changes, err := normalizeFileChanges(args)
	if err != nil {
		return EditFileResult{}, err
	}
	confirmation := ctx.ToolConfirmation()
	if confirmation != nil && !confirmation.Confirmed {
		paths, pathErr := resolvedChangePaths(access, changes)
		if pathErr != nil {
			return EditFileResult{}, pathErr
		}
		return deniedPatchResult(paths, approvalReason(confirmation)), nil
	}

	proposal, err := buildPatchProposalWithAccess(access, changes)
	if err != nil {
		if confirmation != nil && !errors.Is(err, errPathOutsideScope) {
			paths, pathErr := resolvedChangePaths(access, changes)
			if pathErr == nil {
				return editConflictResult(paths), nil
			}
		}
		return EditFileResult{}, err
	}
	if confirmation == nil {
		if permission.ConfirmationMode == toolpolicy.ConfirmationAsk {
			payload := proposal.confirmationPayload()
			if err := ctx.RequestConfirmation(confirmationHint(proposal), payload); err != nil {
				return EditFileResult{}, fmt.Errorf("request file edit approval: %w", err)
			}
			ctx.Actions().SkipSummarization = true
			return patchResult("approval_required", proposal), nil
		}
		if err := applyPatchProposal(proposal.rootPath, proposal); err != nil {
			return EditFileResult{}, err
		}
		return patchResult("applied", proposal), nil
	}

	payload, err := decodeEditConfirmationPayload(confirmation)
	if err != nil {
		return EditFileResult{}, err
	}
	if !proposal.matchesPayload(payload) {
		return editConflictResult(proposal.paths()), nil
	}
	if err := applyPatchProposal(proposal.rootPath, proposal); err != nil {
		if errors.Is(err, errEditConflict) {
			return editConflictResult(proposal.paths()), nil
		}
		return EditFileResult{}, err
	}
	return patchResult("applied", proposal), nil
}

func editFileDeniedResult(access filesystemAccess) deniedResultFunc {
	return func(input map[string]any, confirmation *toolconfirmation.ToolConfirmation) (map[string]any, error) {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encode edit file input: %w", err)
		}
		var args EditFileArgs
		if err := json.Unmarshal(encoded, &args); err != nil {
			return nil, fmt.Errorf("decode edit file input: %w", err)
		}
		changes, err := normalizeFileChanges(args)
		if err != nil {
			return nil, err
		}
		paths, err := resolvedChangePaths(access, changes)
		if err != nil {
			return nil, err
		}
		result := deniedPatchResult(paths, approvalReason(confirmation))
		encoded, err = json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encode denied edit result: %w", err)
		}
		var response map[string]any
		if err := json.Unmarshal(encoded, &response); err != nil {
			return nil, fmt.Errorf("decode denied edit result: %w", err)
		}
		return response, nil
	}
}

func normalizeFileChanges(args EditFileArgs) ([]FileChange, error) {
	changes := args.Changes
	if len(changes) > 0 {
		if strings.TrimSpace(args.Path) != "" || len(args.Edits) > 0 {
			return nil, fmt.Errorf("use either changes or the legacy path and edits fields, not both")
		}
	} else {
		changes = []FileChange{{
			Operation: fileOperationUpdate,
			Path:      args.Path,
			Edits:     args.Edits,
		}}
	}
	if len(changes) > maxPatchFiles {
		return nil, fmt.Errorf("at most %d files may be changed at once", maxPatchFiles)
	}

	normalized := make([]FileChange, 0, len(changes))
	seenPaths := make(map[string]struct{}, len(changes))
	payloadBytes := 0
	for index, change := range changes {
		change.Operation = strings.ToLower(strings.TrimSpace(change.Operation))
		change.Path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(change.Path))))
		if change.Path == "." || change.Path == "" {
			return nil, fmt.Errorf("change %d requires a file path", index+1)
		}
		if _, exists := seenPaths[change.Path]; exists {
			return nil, fmt.Errorf("file %q occurs more than once; combine its updates into one change", change.Path)
		}
		seenPaths[change.Path] = struct{}{}
		payloadBytes += len(change.Path)

		switch change.Operation {
		case fileOperationCreate:
			if len(change.Edits) > 0 {
				return nil, fmt.Errorf("create change %d must use content, not edits", index+1)
			}
			if !validEditText(change.Content) {
				return nil, fmt.Errorf("create change %d content must be UTF-8 text", index+1)
			}
			if len(change.Content) > maxEditFileBytes {
				return nil, fmt.Errorf("created file %q exceeds %d bytes", change.Path, maxEditFileBytes)
			}
			payloadBytes += len(change.Content)
		case fileOperationUpdate:
			if change.Content != "" {
				return nil, fmt.Errorf("update change %d must use edits, not content", index+1)
			}
			if len(change.Edits) == 0 {
				return nil, fmt.Errorf("update change %d requires at least one text replacement", index+1)
			}
			if len(change.Edits) > maxFileEdits {
				return nil, fmt.Errorf("update change %d allows at most %d text replacements", index+1, maxFileEdits)
			}
			for editIndex, edit := range change.Edits {
				if edit.OldText == "" {
					return nil, fmt.Errorf("change %d edit %d oldText must not be empty", index+1, editIndex+1)
				}
				if !validEditText(edit.OldText) || !validEditText(edit.NewText) {
					return nil, fmt.Errorf("change %d edit %d must contain UTF-8 text", index+1, editIndex+1)
				}
				payloadBytes += len(edit.OldText) + len(edit.NewText)
			}
		case fileOperationDelete:
			if change.Content != "" || len(change.Edits) > 0 {
				return nil, fmt.Errorf("delete change %d must not include content or edits", index+1)
			}
		default:
			return nil, fmt.Errorf("change %d operation must be create, update, or delete", index+1)
		}
		if payloadBytes > maxPatchPayloadBytes {
			return nil, fmt.Errorf("file change input exceeds %d bytes", maxPatchPayloadBytes)
		}
		normalized = append(normalized, change)
	}
	return normalized, nil
}

func buildPatchProposal(rootPath string, changes []FileChange) (patchProposal, error) {
	access, err := newFilesystemAccess(rootPath, toolpolicy.ScopeWorkspace)
	if err != nil {
		return patchProposal{}, err
	}
	return buildPatchProposalWithAccess(access, changes)
}

func buildPatchProposalWithAccess(access filesystemAccess, changes []FileChange) (patchProposal, error) {
	resolvedPaths := make([]resolvedPath, len(changes))
	rootPath := ""
	for index, change := range changes {
		resolved, err := access.Resolve(change.Path)
		if err != nil {
			return patchProposal{}, err
		}
		if rootPath == "" {
			rootPath = resolved.RootPath
		} else if rootPath != resolved.RootPath {
			return patchProposal{}, fmt.Errorf("one file batch cannot span filesystem volumes")
		}
		resolvedPaths[index] = resolved
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return patchProposal{}, fmt.Errorf("open filesystem scope: %w", err)
	}
	defer root.Close()

	proposal := patchProposal{rootPath: rootPath, files: make([]filePatchProposal, 0, len(changes))}
	var combinedDiff strings.Builder
	for index, change := range changes {
		resolved := resolvedPaths[index]
		before, err := readOptionalEditSnapshot(root, resolved.RootRelative)
		if err != nil {
			return patchProposal{}, err
		}
		var after []byte
		switch change.Operation {
		case fileOperationCreate:
			if before.exists {
				return patchProposal{}, fmt.Errorf("create %q: file already exists", resolved.Display)
			}
			parentInfo, err := root.Stat(path.Dir(resolved.RootRelative))
			if err != nil {
				return patchProposal{}, fmt.Errorf("inspect parent directory for %q: %w", resolved.Display, err)
			}
			if !parentInfo.IsDir() {
				return patchProposal{}, fmt.Errorf("parent of %q is not a directory", resolved.Display)
			}
			after = []byte(change.Content)
		case fileOperationUpdate:
			if !before.exists {
				return patchProposal{}, fmt.Errorf("update %q: file does not exist", resolved.Display)
			}
			after, err = applyTextReplacements(before.content, resolved.Display, change.Edits)
			if err != nil {
				return patchProposal{}, err
			}
		case fileOperationDelete:
			if !before.exists {
				return patchProposal{}, fmt.Errorf("delete %q: file does not exist", resolved.Display)
			}
		}

		fileProposal := filePatchProposal{
			operation:   change.Operation,
			path:        resolved.RootRelative,
			displayPath: resolved.Display,
			before:      before,
			after:       after,
			diff:        fileUnifiedDiff(change.Operation, resolved.Display, before.content, after),
			editCount:   len(change.Edits),
		}
		if change.Operation != fileOperationDelete {
			fileProposal.afterHash = contentSHA256(after)
		}
		if index > 0 {
			combinedDiff.WriteByte('\n')
		}
		combinedDiff.WriteString(fileProposal.diff)
		if !strings.HasSuffix(fileProposal.diff, "\n") {
			combinedDiff.WriteByte('\n')
		}
		proposal.files = append(proposal.files, fileProposal)
		proposal.editCount += fileProposal.editCount
	}
	proposal.diff = combinedDiff.String()
	return proposal, nil
}

func buildEditProposal(rootPath, filePath string, edits []TextReplacement) (filePatchProposal, error) {
	changes, err := normalizeFileChanges(EditFileArgs{Path: filePath, Edits: edits})
	if err != nil {
		return filePatchProposal{}, err
	}
	proposal, err := buildPatchProposal(rootPath, changes)
	if err != nil {
		return filePatchProposal{}, err
	}
	return proposal.files[0], nil
}

func resolvedChangePaths(access filesystemAccess, changes []FileChange) ([]string, error) {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		resolved, err := access.Resolve(change.Path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, resolved.Display)
	}
	return paths, nil
}

func applyTextReplacements(content []byte, filePath string, edits []TextReplacement) ([]byte, error) {
	updated := string(content)
	for index, edit := range edits {
		matches := strings.Count(updated, edit.OldText)
		if matches == 0 {
			return nil, fmt.Errorf("edit %d oldText was not found in %q", index+1, filePath)
		}
		if matches > 1 {
			return nil, fmt.Errorf("edit %d oldText occurs %d times in %q; include more surrounding context", index+1, matches, filePath)
		}
		updated = strings.Replace(updated, edit.OldText, edit.NewText, 1)
		if len(updated) > maxEditFileBytes {
			return nil, fmt.Errorf("edited file exceeds %d bytes", maxEditFileBytes)
		}
	}
	after := []byte(updated)
	if bytes.Equal(content, after) {
		return nil, fmt.Errorf("the proposed edits do not change %q", filePath)
	}
	return after, nil
}

func readOptionalEditSnapshot(root *os.Root, filePath string) (editSnapshot, error) {
	linkInfo, err := root.Lstat(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return editSnapshot{}, nil
	}
	if err != nil {
		return editSnapshot{}, fmt.Errorf("inspect %q: %w", filePath, err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return editSnapshot{}, fmt.Errorf("%q is a symbolic link", filePath)
	}
	file, err := root.Open(filePath)
	if err != nil {
		return editSnapshot{}, fmt.Errorf("open file %q: %w", filePath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return editSnapshot{}, fmt.Errorf("inspect %q: %w", filePath, err)
	}
	if !info.Mode().IsRegular() {
		return editSnapshot{}, fmt.Errorf("%q is not a regular file", filePath)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxEditFileBytes+1))
	if err != nil {
		return editSnapshot{}, fmt.Errorf("read %q: %w", filePath, err)
	}
	if len(data) > maxEditFileBytes {
		return editSnapshot{}, fmt.Errorf("%q exceeds %d bytes", filePath, maxEditFileBytes)
	}
	if !validEditText(string(data)) {
		return editSnapshot{}, fmt.Errorf("%q is not a UTF-8 text file", filePath)
	}
	return editSnapshot{
		exists:  true,
		content: data,
		mode:    info.Mode(),
		hash:    contentSHA256(data),
	}, nil
}

func applyPatchProposal(rootPath string, proposal patchProposal) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open workspace: %w", err)
	}
	defer root.Close()

	entries := make([]patchTransactionEntry, len(proposal.files))
	for index, file := range proposal.files {
		entries[index].file = file
		if file.operation == fileOperationDelete {
			continue
		}
		stagedPath, err := stagePatchFile(root, file)
		if err != nil {
			cleanupPatchArtifacts(root, entries)
			return err
		}
		entries[index].stagedPath = stagedPath
	}
	if err := validatePatchState(root, proposal); err != nil {
		cleanupPatchArtifacts(root, entries)
		return err
	}

	for index := range entries {
		entry := &entries[index]
		if !entry.file.before.exists {
			continue
		}
		if err := validateFileState(root, entry.file); err != nil {
			return rollbackPatch(root, entries, err)
		}
		entry.backupPath = temporarySiblingPath(entry.file.path, "bak")
		if err := root.Rename(entry.file.path, entry.backupPath); err != nil {
			return rollbackPatch(root, entries, fmt.Errorf("prepare %q for replacement: %w", entry.file.path, err))
		}
		entry.originalMoved = true
	}

	for index := range entries {
		entry := &entries[index]
		switch entry.file.operation {
		case fileOperationCreate:
			if err := root.Link(entry.stagedPath, entry.file.path); err != nil {
				if errors.Is(err, fs.ErrExist) {
					err = fmt.Errorf("%w: create %q: target now exists", errEditConflict, entry.file.path)
				} else {
					err = fmt.Errorf("create %q: %w", entry.file.path, err)
				}
				return rollbackPatch(root, entries, err)
			}
			entry.installed = true
		case fileOperationUpdate:
			if err := root.Rename(entry.stagedPath, entry.file.path); err != nil {
				return rollbackPatch(root, entries, fmt.Errorf("replace %q: %w", entry.file.path, err))
			}
			entry.installed = true
		case fileOperationDelete:
			// Moving the original to its backup applies the delete. The backup is removed below.
		}
	}

	var cleanupErrors []error
	for _, entry := range entries {
		if entry.stagedPath != "" {
			if err := removeIfExists(root, entry.stagedPath); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if entry.backupPath != "" {
			if err := removeIfExists(root, entry.backupPath); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return fmt.Errorf("patch applied but temporary file cleanup failed: %w", err)
	}
	return nil
}

func stagePatchFile(root *os.Root, proposal filePatchProposal) (string, error) {
	temporaryPath := temporarySiblingPath(proposal.path, "tmp")
	mode := os.FileMode(0o644)
	if proposal.before.exists {
		mode = proposal.before.mode.Perm()
	}
	temporary, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", fmt.Errorf("create temporary file for %q: %w", proposal.path, err)
	}
	if _, err := temporary.Write(proposal.after); err != nil {
		_ = temporary.Close()
		_ = root.Remove(temporaryPath)
		return "", fmt.Errorf("write temporary file for %q: %w", proposal.path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = root.Remove(temporaryPath)
		return "", fmt.Errorf("sync temporary file for %q: %w", proposal.path, err)
	}
	if err := temporary.Close(); err != nil {
		_ = root.Remove(temporaryPath)
		return "", fmt.Errorf("close temporary file for %q: %w", proposal.path, err)
	}
	return temporaryPath, nil
}

func validatePatchState(root *os.Root, proposal patchProposal) error {
	for _, file := range proposal.files {
		if err := validateFileState(root, file); err != nil {
			return err
		}
	}
	return nil
}

func validateFileState(root *os.Root, proposal filePatchProposal) error {
	current, err := readOptionalEditSnapshot(root, proposal.path)
	if err != nil {
		return fmt.Errorf("%w: verify %q: %v", errEditConflict, proposal.path, err)
	}
	if proposal.operation == fileOperationCreate {
		if current.exists {
			return fmt.Errorf("%w: create %q: target now exists", errEditConflict, proposal.path)
		}
		return nil
	}
	if !current.exists || current.hash != proposal.before.hash || current.mode.Perm() != proposal.before.mode.Perm() {
		return fmt.Errorf("%w: %q", errEditConflict, proposal.path)
	}
	return nil
}

func rollbackPatch(root *os.Root, entries []patchTransactionEntry, cause error) error {
	var rollbackErrors []error
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.installed {
			if err := removeIfExists(root, entry.file.path); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove partially applied %q: %w", entry.file.path, err))
			}
		}
		if entry.originalMoved {
			if err := root.Rename(entry.backupPath, entry.file.path); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %q: %w", entry.file.path, err))
			}
		}
	}
	for _, entry := range entries {
		if entry.stagedPath != "" {
			_ = removeIfExists(root, entry.stagedPath)
		}
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		return fmt.Errorf("%w after %v: %v", errPatchRollback, cause, err)
	}
	return cause
}

func cleanupPatchArtifacts(root *os.Root, entries []patchTransactionEntry) {
	for _, entry := range entries {
		if entry.stagedPath != "" {
			_ = removeIfExists(root, entry.stagedPath)
		}
		if entry.backupPath != "" {
			_ = removeIfExists(root, entry.backupPath)
		}
	}
}

func removeIfExists(root *os.Root, filePath string) error {
	err := root.Remove(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func temporarySiblingPath(filePath, extension string) string {
	return path.Join(
		path.Dir(filePath),
		"."+path.Base(filePath)+".materialmind-"+uuid.NewString()+"."+extension,
	)
}

func fileUnifiedDiff(operation, filePath string, before, after []byte) string {
	oldPath := "a/" + filePath
	newPath := "b/" + filePath
	if operation == fileOperationCreate {
		oldPath = "/dev/null"
	}
	if operation == fileOperationDelete {
		newPath = "/dev/null"
	}
	diff := udiff.Unified(oldPath, newPath, string(before), string(after))
	if diff == "" {
		return fmt.Sprintf("--- %s\n+++ %s\n", oldPath, newPath)
	}
	return diff
}

func (proposal patchProposal) confirmationPayload() editConfirmationPayload {
	payload := editConfirmationPayload{
		Kind:  "file_patch",
		Diff:  proposal.diff,
		Files: make([]fileApprovalProposal, 0, len(proposal.files)),
	}
	for _, file := range proposal.files {
		payload.Files = append(payload.Files, fileApprovalProposal{
			Operation:    file.operation,
			Path:         file.displayPath,
			Diff:         file.diff,
			BaseSHA256:   file.before.hash,
			BaseMode:     uint32(file.before.mode.Perm()),
			ResultSHA256: file.afterHash,
		})
	}
	return payload
}

func (proposal patchProposal) matchesPayload(payload editConfirmationPayload) bool {
	if payload.Kind != "file_patch" || payload.Diff != proposal.diff || len(payload.Files) != len(proposal.files) {
		return false
	}
	for index, file := range proposal.files {
		expected := payload.Files[index]
		if expected.Operation != file.operation ||
			expected.Path != file.displayPath ||
			expected.Diff != file.diff ||
			expected.BaseSHA256 != file.before.hash ||
			expected.BaseMode != uint32(file.before.mode.Perm()) ||
			expected.ResultSHA256 != file.afterHash {
			return false
		}
	}
	return true
}

func decodeEditConfirmationPayload(confirmation *toolconfirmation.ToolConfirmation) (editConfirmationPayload, error) {
	if confirmation == nil || confirmation.Payload == nil {
		return editConfirmationPayload{}, fmt.Errorf("file edit approval payload is missing")
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return editConfirmationPayload{}, fmt.Errorf("encode file edit approval payload: %w", err)
	}
	var payload editConfirmationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return editConfirmationPayload{}, fmt.Errorf("decode file edit approval payload: %w", err)
	}
	return payload, nil
}

func confirmationHint(proposal patchProposal) string {
	if len(proposal.files) == 1 {
		file := proposal.files[0]
		return fmt.Sprintf("Allow the agent to %s %s?", file.operation, file.displayPath)
	}
	return fmt.Sprintf("Allow the agent to change %d files?", len(proposal.files))
}

func patchResult(state string, proposal patchProposal) EditFileResult {
	paths := proposal.paths()
	result := EditFileResult{
		State:       state,
		Paths:       paths,
		Diff:        proposal.diff,
		ChangeCount: len(proposal.files),
		EditCount:   proposal.editCount,
	}
	if len(paths) == 1 {
		result.Path = paths[0]
	}
	return result
}

func deniedPatchResult(paths []string, reason string) EditFileResult {
	result := EditFileResult{
		State:       "denied",
		Paths:       paths,
		ChangeCount: len(paths),
		Reason:      reason,
	}
	if len(paths) == 1 {
		result.Path = paths[0]
	}
	return result
}

func (proposal patchProposal) paths() []string {
	paths := make([]string, 0, len(proposal.files))
	for _, file := range proposal.files {
		paths = append(paths, file.displayPath)
	}
	return paths
}

func validEditText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func contentSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func editConflictResult(paths []string) EditFileResult {
	result := EditFileResult{
		State:       "conflict",
		Paths:       paths,
		ChangeCount: len(paths),
		Reason:      "One or more files changed after this patch was proposed. Read them again and propose a new patch.",
		Conflict:    true,
	}
	if len(paths) == 1 {
		result.Path = paths[0]
	}
	return result
}
