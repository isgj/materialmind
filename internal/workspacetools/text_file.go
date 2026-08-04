package workspacetools

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"

	"materialmind/internal/toolpolicy"
)

// TextFileRead is a scope-checked read prepared for a protocol client request.
// Preparing and executing are separate so callers can obtain approval before
// any file content is read.
type TextFileRead struct {
	resolved resolvedPath
	args     ReadFileArgs
}

func PrepareTextFileRead(
	workspaceRoot string,
	permission toolpolicy.Permission,
	filePath string,
	line, limit *int,
) (TextFileRead, error) {
	access, err := newFilesystemAccess(workspaceRoot, permission.FilesystemScope)
	if err != nil {
		return TextFileRead{}, err
	}
	resolved, err := access.Resolve(filePath)
	if err != nil {
		return TextFileRead{}, err
	}
	if resolved.Display == "." {
		return TextFileRead{}, fmt.Errorf("file path is required")
	}
	start := 1
	if line != nil {
		start = *line
	}
	if start < 1 {
		return TextFileRead{}, fmt.Errorf("line must be at least 1")
	}
	lineLimit := maxReadLines
	if limit != nil {
		if *limit < 1 {
			return TextFileRead{}, fmt.Errorf("limit must be at least 1")
		}
		lineLimit = min(*limit, maxReadLines)
	}
	maxInt := int(^uint(0) >> 1)
	if start > maxInt-lineLimit {
		return TextFileRead{}, fmt.Errorf("line is too large")
	}
	return TextFileRead{
		resolved: resolved,
		args: ReadFileArgs{
			Path:      filePath,
			StartLine: start,
			EndLine:   start + lineLimit - 1,
		},
	}, nil
}

func (r TextFileRead) Path() string {
	return r.resolved.Display
}

func (r TextFileRead) AbsolutePath() string {
	return r.resolved.Absolute
}

func (r TextFileRead) Execute() (ReadFileResult, error) {
	return readResolved(r.resolved, r.args)
}

type TextFileWritePreview struct {
	Operation    string `json:"operation"`
	Path         string `json:"path"`
	AbsolutePath string `json:"absolutePath"`
	Diff         string `json:"diff"`
	Noop         bool   `json:"noop"`
}

// TextFileWrite is an immutable, scope-checked full-file replacement. Apply
// verifies the original content again to prevent approval-time races.
type TextFileWrite struct {
	proposal patchProposal
	preview  TextFileWritePreview
}

func PrepareTextFileWrite(
	workspaceRoot string,
	permission toolpolicy.Permission,
	filePath, content string,
) (TextFileWrite, error) {
	if len(content) > maxEditFileBytes {
		return TextFileWrite{}, fmt.Errorf("file content exceeds %d bytes", maxEditFileBytes)
	}
	if !validEditText(content) {
		return TextFileWrite{}, fmt.Errorf("file content must be UTF-8 text")
	}
	access, err := newFilesystemAccess(workspaceRoot, permission.FilesystemScope)
	if err != nil {
		return TextFileWrite{}, err
	}
	resolved, err := access.Resolve(filePath)
	if err != nil {
		return TextFileWrite{}, err
	}
	if resolved.Display == "." {
		return TextFileWrite{}, fmt.Errorf("file path is required")
	}
	root, err := os.OpenRoot(resolved.RootPath)
	if err != nil {
		return TextFileWrite{}, fmt.Errorf("open filesystem scope: %w", err)
	}
	defer root.Close()
	before, err := readOptionalEditSnapshot(root, resolved.RootRelative)
	if err != nil {
		return TextFileWrite{}, err
	}
	operation := fileOperationUpdate
	if !before.exists {
		operation = fileOperationCreate
		parentInfo, statErr := root.Stat(path.Dir(resolved.RootRelative))
		if statErr != nil {
			return TextFileWrite{}, fmt.Errorf("inspect parent directory for %q: %w", resolved.Display, statErr)
		}
		if !parentInfo.IsDir() {
			return TextFileWrite{}, fmt.Errorf("parent of %q is not a directory", resolved.Display)
		}
	}
	after := []byte(content)
	noop := before.exists && bytes.Equal(before.content, after)
	file := filePatchProposal{
		operation:   operation,
		path:        resolved.RootRelative,
		displayPath: resolved.Display,
		before:      before,
		after:       after,
		diff:        fileUnifiedDiff(operation, resolved.Display, before.content, after),
		afterHash:   contentSHA256(after),
		editCount:   1,
	}
	proposal := patchProposal{
		rootPath:  resolved.RootPath,
		files:     []filePatchProposal{file},
		diff:      file.diff,
		editCount: 1,
	}
	return TextFileWrite{
		proposal: proposal,
		preview: TextFileWritePreview{
			Operation:    operation,
			Path:         resolved.Display,
			AbsolutePath: resolved.Absolute,
			Diff:         file.diff,
			Noop:         noop,
		},
	}, nil
}

func (w TextFileWrite) Preview() TextFileWritePreview {
	return w.preview
}

func (w TextFileWrite) Apply() error {
	if w.preview.Noop {
		return nil
	}
	if strings.TrimSpace(w.proposal.rootPath) == "" || len(w.proposal.files) != 1 {
		return fmt.Errorf("text file write is not prepared")
	}
	return applyPatchProposal(w.proposal.rootPath, w.proposal)
}
