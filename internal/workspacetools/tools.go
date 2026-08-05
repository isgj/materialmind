package workspacetools

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/agentskills"
	"materialmind/internal/toolpolicy"
)

const (
	maxDirectoryEntries = 500
	maxReadBytes        = 256 * 1024
	maxReadLines        = 400
)

type ListDirectoryArgs struct {
	Path string `json:"path" jsonschema:"Directory path. Relative paths start at the workspace root; use . for the workspace root."`
}

type DirectoryEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type ListDirectoryResult struct {
	State     string           `json:"state,omitempty"`
	Path      string           `json:"path"`
	Entries   []DirectoryEntry `json:"entries"`
	Truncated bool             `json:"truncated"`
	Reason    string           `json:"reason,omitempty"`
}

type ReadFileArgs struct {
	Path      string `json:"path" jsonschema:"File path. Relative paths start at the workspace root."`
	StartLine int    `json:"startLine,omitempty" jsonschema:"Optional 1-based first line to read. Defaults to 1. Use with endLine to read a specific section instead of running sed, head, or tail."`
	EndLine   int    `json:"endLine,omitempty" jsonschema:"Optional inclusive last line to read. Defaults to startLine plus 399. At most 400 lines are returned."`
}

type ReadFileResult struct {
	State     string `json:"state,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason,omitempty"`
}

type Options struct {
	CommandOutput      CommandOutputSink
	CommandResult      CommandResultSink
	RequestApproval    ToolApprovalHandler
	AskUser            AskUserHandler
	SessionNotes       SessionNotesHandlers
	YieldAfterApproval func(agent.Context) bool
}

type filesystemConfirmationPayload struct {
	Kind         string `json:"kind"`
	Operation    string `json:"operation"`
	Path         string `json:"path"`
	AbsolutePath string `json:"absolutePath"`
}

func New(rootPath string, permissions []toolpolicy.Permission, skillCatalog agentskills.Catalog, providedOptions ...Options) ([]tool.Tool, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if len(providedOptions) > 1 {
		return nil, fmt.Errorf("at most one workspace tool options value is supported")
	}
	options := Options{}
	if len(providedOptions) == 1 {
		options = providedOptions[0]
	}
	permissions, err := toolpolicy.NormalizePermissions(permissions)
	if err != nil {
		return nil, fmt.Errorf("validate tool permissions: %w", err)
	}
	listPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolListDirectory)
	listAccess, err := newFilesystemAccess(rootPath, listPermission.FilesystemScope)
	if err != nil {
		return nil, fmt.Errorf("configure list_directory access: %w", err)
	}
	listDirectoryBase, err := functiontool.New(
		functiontool.Config{
			Name:        toolpolicy.ToolListDirectory,
			Description: "List files and directories. Entries are returned in name order. " + listAccess.Description(),
		},
		func(ctx agent.Context, args ListDirectoryArgs) (ListDirectoryResult, error) {
			return listWithPolicy(listAccess, listPermission, ctx, args)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create list_directory tool: %w", err)
	}
	listDirectory, err := newApprovalAwareTool(listDirectoryBase, filesystemDeniedResult(listAccess, "list"))
	if err != nil {
		return nil, fmt.Errorf("configure list_directory approval: %w", err)
	}
	readPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolReadFile)
	readAccess, err := newFilesystemAccess(rootPath, readPermission.FilesystemScope)
	if err != nil {
		return nil, fmt.Errorf("configure read_file access: %w", err)
	}
	readFileBase, err := functiontool.New(
		functiontool.Config{
			Name:        toolpolicy.ToolReadFile,
			Description: "Read up to 400 lines from a UTF-8 text file. Set startLine and endLine for a specific section; use this instead of running sed, head, or tail. " + readAccess.Description(),
		},
		func(ctx agent.Context, args ReadFileArgs) (ReadFileResult, error) {
			return readWithPolicy(readAccess, readPermission, ctx, args)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create read_file tool: %w", err)
	}
	readFile, err := newApprovalAwareTool(readFileBase, filesystemDeniedResult(readAccess, "read"))
	if err != nil {
		return nil, fmt.Errorf("configure read_file approval: %w", err)
	}
	grepPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolGrep)
	grepTool, err := newGrepTool(rootPath, grepPermission)
	if err != nil {
		return nil, fmt.Errorf("create grep tool: %w", err)
	}
	fetchPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolFetchURL)
	fetchURL, err := newFetchTool(fetchPermission)
	if err != nil {
		return nil, fmt.Errorf("create fetch_url tool: %w", err)
	}
	editPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolEditFile)
	editFile, err := newEditFileTool(rootPath, editPermission)
	if err != nil {
		return nil, fmt.Errorf("create edit_file tool: %w", err)
	}
	loadSkillPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolLoadSkill)
	loadSkill, err := newLoadSkillTool(skillCatalog, loadSkillPermission)
	if err != nil {
		return nil, fmt.Errorf("create load_skill tool: %w", err)
	}
	readSessionNotesPermission, _ := toolpolicy.PermissionFor(
		permissions,
		toolpolicy.ToolReadSessionNotes,
	)
	readSessionNotes, err := newReadSessionNotesTool(
		options.SessionNotes.Read,
		readSessionNotesPermission,
	)
	if err != nil {
		return nil, fmt.Errorf("create read_session_notes tool: %w", err)
	}
	updateSessionNotesPermission, _ := toolpolicy.PermissionFor(
		permissions,
		toolpolicy.ToolUpdateSessionNotes,
	)
	updateSessionNotes, err := newUpdateSessionNotesTool(
		options.SessionNotes.Update,
		updateSessionNotesPermission,
	)
	if err != nil {
		return nil, fmt.Errorf("create update_session_notes tool: %w", err)
	}
	runCommandPermission, _ := toolpolicy.PermissionFor(permissions, toolpolicy.ToolRunCommand)
	runCommand, err := newRunCommandTool(
		rootPath,
		runCommandPermission,
		options.CommandOutput,
		options.CommandResult,
		options.RequestApproval,
	)
	if err != nil {
		return nil, fmt.Errorf("create run_command tool: %w", err)
	}
	askUser, err := newAskUserTool(options.AskUser)
	if err != nil {
		return nil, fmt.Errorf("create ask_user tool: %w", err)
	}
	tools := []tool.Tool{listDirectory, readFile}
	if grepTool != nil {
		tools = append(tools, grepTool)
	}
	tools = append(
		tools,
		fetchURL,
		editFile,
		loadSkill,
		readSessionNotes,
		updateSessionNotes,
		runCommand,
		askUser,
	)
	if options.YieldAfterApproval == nil {
		return tools, nil
	}
	for index, workspaceTool := range tools {
		wrapped, wrapErr := newApprovalYieldTool(workspaceTool, options.YieldAfterApproval)
		if wrapErr != nil {
			return nil, fmt.Errorf("configure %s approval scheduling: %w", workspaceTool.Name(), wrapErr)
		}
		tools[index] = wrapped
	}
	return tools, nil
}

func list(rootPath string, args ListDirectoryArgs) (ListDirectoryResult, error) {
	access, err := newFilesystemAccess(rootPath, toolpolicy.ScopeWorkspace)
	if err != nil {
		return ListDirectoryResult{}, err
	}
	return listWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolListDirectory,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, nil, args)
}

func listWithPolicy(access filesystemAccess, permission toolpolicy.Permission, ctx agent.Context, args ListDirectoryArgs) (ListDirectoryResult, error) {
	resolved, err := access.Resolve(args.Path)
	if err != nil {
		return ListDirectoryResult{}, err
	}
	if ctx != nil && permission.ConfirmationMode == toolpolicy.ConfirmationAsk && ctx.ToolConfirmation() == nil {
		if err := requestFilesystemConfirmation(ctx, "list", resolved); err != nil {
			return ListDirectoryResult{}, err
		}
		return ListDirectoryResult{State: "approval_required", Path: resolved.Display, Entries: []DirectoryEntry{}}, nil
	}
	if ctx != nil && ctx.ToolConfirmation() != nil && !ctx.ToolConfirmation().Confirmed {
		return ListDirectoryResult{State: "denied", Path: resolved.Display, Entries: []DirectoryEntry{}, Reason: approvalReason(ctx.ToolConfirmation())}, nil
	}
	return listResolved(resolved)
}

func listResolved(resolved resolvedPath) (ListDirectoryResult, error) {
	root, err := os.OpenRoot(resolved.RootPath)
	if err != nil {
		return ListDirectoryResult{}, fmt.Errorf("open filesystem scope: %w", err)
	}
	defer root.Close()
	directory, err := root.Open(resolved.RootRelative)
	if err != nil {
		return ListDirectoryResult{}, fmt.Errorf("open directory %q: %w", resolved.Display, err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return ListDirectoryResult{}, fmt.Errorf("inspect %q: %w", resolved.Display, err)
	}
	if !info.IsDir() {
		return ListDirectoryResult{}, fmt.Errorf("%q is not a directory", resolved.Display)
	}
	entries, err := directory.ReadDir(maxDirectoryEntries + 1)
	if err != nil && err != io.EOF {
		return ListDirectoryResult{}, fmt.Errorf("list %q: %w", resolved.Display, err)
	}
	result := ListDirectoryResult{State: "listed", Path: resolved.Display}
	if len(entries) > maxDirectoryEntries {
		entries = entries[:maxDirectoryEntries]
		result.Truncated = true
	}
	result.Entries = make([]DirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		item := DirectoryEntry{Name: entry.Name(), Type: "file"}
		entryInfo, statErr := entry.Info()
		if statErr == nil {
			item.Size = entryInfo.Size()
			switch {
			case entryInfo.IsDir():
				item.Type = "directory"
			case entryInfo.Mode()&os.ModeSymlink != 0:
				item.Type = "symlink"
			case !entryInfo.Mode().IsRegular():
				item.Type = "other"
			}
		}
		result.Entries = append(result.Entries, item)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	return result, nil
}

func read(rootPath string, args ReadFileArgs) (ReadFileResult, error) {
	access, err := newFilesystemAccess(rootPath, toolpolicy.ScopeWorkspace)
	if err != nil {
		return ReadFileResult{}, err
	}
	return readWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolReadFile,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, nil, args)
}

func readWithPolicy(access filesystemAccess, permission toolpolicy.Permission, ctx agent.Context, args ReadFileArgs) (ReadFileResult, error) {
	resolved, err := access.Resolve(args.Path)
	if err != nil {
		return ReadFileResult{}, err
	}
	if resolved.Display == "." {
		return ReadFileResult{}, fmt.Errorf("file path is required")
	}
	if ctx != nil && permission.ConfirmationMode == toolpolicy.ConfirmationAsk && ctx.ToolConfirmation() == nil {
		if err := requestFilesystemConfirmation(ctx, "read", resolved); err != nil {
			return ReadFileResult{}, err
		}
		return ReadFileResult{State: "approval_required", Path: resolved.Display}, nil
	}
	if ctx != nil && ctx.ToolConfirmation() != nil && !ctx.ToolConfirmation().Confirmed {
		return ReadFileResult{State: "denied", Path: resolved.Display, Reason: approvalReason(ctx.ToolConfirmation())}, nil
	}
	return readResolved(resolved, args)
}

func readResolved(resolved resolvedPath, args ReadFileArgs) (ReadFileResult, error) {
	start := args.StartLine
	if start == 0 {
		start = 1
	}
	if start < 1 {
		return ReadFileResult{}, fmt.Errorf("startLine must be at least 1")
	}
	end := args.EndLine
	if end == 0 || end >= start+maxReadLines {
		end = start + maxReadLines - 1
	}
	if end < 0 {
		return ReadFileResult{}, fmt.Errorf("endLine must be at least 1")
	}
	if end < start {
		return ReadFileResult{}, fmt.Errorf("endLine must not be before startLine")
	}

	root, err := os.OpenRoot(resolved.RootPath)
	if err != nil {
		return ReadFileResult{}, fmt.Errorf("open filesystem scope: %w", err)
	}
	defer root.Close()
	file, err := root.Open(resolved.RootRelative)
	if err != nil {
		return ReadFileResult{}, fmt.Errorf("open file %q: %w", resolved.Display, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ReadFileResult{}, fmt.Errorf("inspect %q: %w", resolved.Display, err)
	}
	if !info.Mode().IsRegular() {
		return ReadFileResult{}, fmt.Errorf("%q is not a regular file", resolved.Display)
	}

	result := ReadFileResult{State: "read", Path: resolved.Display, StartLine: start, EndLine: start - 1}
	var selected strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxReadBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if bytes.IndexByte(line, 0) >= 0 {
			return ReadFileResult{}, fmt.Errorf("%q appears to be a binary file", resolved.Display)
		}
		if !utf8.Valid(line) {
			return ReadFileResult{}, fmt.Errorf("%q is not UTF-8 text", resolved.Display)
		}
		if lineNumber < start {
			continue
		}
		if lineNumber > end {
			result.Truncated = true
			break
		}
		if selected.Len()+len(line)+1 > maxReadBytes {
			result.Truncated = true
			break
		}
		selected.Write(line)
		selected.WriteByte('\n')
		result.EndLine = lineNumber
	}
	if err := scanner.Err(); err != nil {
		return ReadFileResult{}, fmt.Errorf("scan %q: %w", resolved.Display, err)
	}
	result.Content = selected.String()
	return result, nil
}

func requestFilesystemConfirmation(ctx agent.Context, operation string, resolved resolvedPath) error {
	verb := "access"
	if operation == "list" {
		verb = "list"
	} else if operation == "read" {
		verb = "read"
	} else if operation == "search" {
		verb = "search"
	}
	if err := ctx.RequestConfirmation(
		fmt.Sprintf("Allow the agent to %s %s?", verb, resolved.Absolute),
		filesystemConfirmationPayload{
			Kind:         "filesystem_access",
			Operation:    operation,
			Path:         resolved.Display,
			AbsolutePath: resolved.Absolute,
		},
	); err != nil {
		return fmt.Errorf("request filesystem approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func filesystemDeniedResult(access filesystemAccess, operation string) deniedResultFunc {
	return func(input map[string]any, confirmation *toolconfirmation.ToolConfirmation) (map[string]any, error) {
		rawPath, _ := input["path"].(string)
		resolved, err := access.Resolve(rawPath)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"state":     "denied",
			"operation": operation,
			"path":      resolved.Display,
			"reason":    approvalReason(confirmation),
		}, nil
	}
}

func cleanRelativePath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" || value == "/" {
		return "."
	}
	return strings.TrimPrefix(value, "/")
}
