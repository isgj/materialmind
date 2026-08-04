package workspacetools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"materialmind/internal/toolpolicy"
)

var errPathOutsideScope = errors.New("path is outside the configured filesystem scope")

type filesystemAccess struct {
	workspaceRoot string
	boundaryRoot  string
	scope         toolpolicy.FilesystemScope
}

type resolvedPath struct {
	RootPath     string
	RootRelative string
	Display      string
	Absolute     string
}

func newFilesystemAccess(workspaceRoot string, scope toolpolicy.FilesystemScope) (filesystemAccess, error) {
	workspaceRoot = filepath.Clean(strings.TrimSpace(workspaceRoot))
	if workspaceRoot == "" || !filepath.IsAbs(workspaceRoot) {
		return filesystemAccess{}, fmt.Errorf("workspace root must be an absolute path")
	}
	access := filesystemAccess{workspaceRoot: workspaceRoot, scope: scope}
	switch scope {
	case toolpolicy.ScopeWorkspace:
		access.boundaryRoot = workspaceRoot
	case toolpolicy.ScopeRepository:
		repositoryRoot, ok := toolpolicy.FindRepositoryRoot(workspaceRoot)
		if !ok {
			return filesystemAccess{}, fmt.Errorf("repository root is unavailable for workspace %q", workspaceRoot)
		}
		access.boundaryRoot = repositoryRoot
	case toolpolicy.ScopeComputer:
		// The boundary is chosen per target so absolute paths on other Windows volumes remain addressable.
	case "":
		return filesystemAccess{}, fmt.Errorf("filesystem scope is required")
	default:
		return filesystemAccess{}, fmt.Errorf("unsupported filesystem scope %q", scope)
	}
	return access, nil
}

func (access filesystemAccess) Resolve(rawPath string) (resolvedPath, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		rawPath = "."
	}
	nativePath := filepath.FromSlash(rawPath)
	absolute := nativePath
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(access.workspaceRoot, nativePath)
	}
	absolute = filepath.Clean(absolute)

	boundary := access.boundaryRoot
	if access.scope == toolpolicy.ScopeComputer {
		boundary = volumeRoot(absolute)
	}
	relative, err := filepath.Rel(boundary, absolute)
	if err != nil || pathEscapes(relative) {
		return resolvedPath{}, fmt.Errorf("%w: %q", errPathOutsideScope, filepath.ToSlash(rawPath))
	}
	if relative == "" {
		relative = "."
	}
	display, displayErr := filepath.Rel(access.workspaceRoot, absolute)
	if displayErr != nil {
		display = absolute
	}
	if display == "" {
		display = "."
	}
	if filepath.IsAbs(nativePath) {
		display = absolute
	}
	return resolvedPath{
		RootPath:     boundary,
		RootRelative: filepath.ToSlash(relative),
		Display:      filepath.ToSlash(display),
		Absolute:     filepath.Clean(absolute),
	}, nil
}

func (access filesystemAccess) Description() string {
	switch access.scope {
	case toolpolicy.ScopeWorkspace:
		return fmt.Sprintf("Paths are relative to the workspace %s and cannot leave it.", access.workspaceRoot)
	case toolpolicy.ScopeRepository:
		return fmt.Sprintf("Paths are relative to workspace %s and may use .. up to repository root %s.", access.workspaceRoot, access.boundaryRoot)
	case toolpolicy.ScopeComputer:
		return fmt.Sprintf("Relative paths start at workspace %s; absolute paths may address any file permitted by the operating system.", access.workspaceRoot)
	default:
		return "Paths are relative to the workspace."
	}
}

func volumeRoot(path string) string {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return string(os.PathSeparator)
	}
	return volume + string(os.PathSeparator)
}

func pathEscapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative)
}
