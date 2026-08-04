package workspacetools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"materialmind/internal/toolpolicy"
)

const (
	defaultGrepMaxResults = 100
	maxGrepResults        = 500
	maxGrepPatternBytes   = 4096
	maxGrepGlobBytes      = 512
	maxGrepGlobs          = 32
	maxGrepJSONEventBytes = 1024 * 1024
	maxGrepTextBytes      = 4096
	maxGrepMatchBytes     = 1024
	defaultGrepTimeout    = 30 * time.Second
)

type GrepArgs struct {
	Pattern      string   `json:"pattern" jsonschema:"Required ripgrep regular expression. Set fixedStrings to search for literal text."`
	Path         string   `json:"path,omitempty" jsonschema:"File or directory to search. Relative paths start at the workspace root. Defaults to the workspace root."`
	Globs        []string `json:"globs,omitempty" jsonschema:"Optional ripgrep glob filters, for example **/*.go or !vendor/**."`
	CaseMode     string   `json:"caseMode,omitempty" jsonschema:"Case matching mode: smart, sensitive, or insensitive. Defaults to smart."`
	FixedStrings bool     `json:"fixedStrings,omitempty" jsonschema:"Treat pattern as literal text instead of a regular expression."`
	MaxResults   int      `json:"maxResults,omitempty" jsonschema:"Maximum matching lines to return. Defaults to 100 and cannot exceed 500."`
}

type GrepMatch struct {
	Path          string `json:"path"`
	Line          int    `json:"line"`
	Column        int    `json:"column"`
	Text          string `json:"text"`
	Match         string `json:"match"`
	TextTruncated bool   `json:"textTruncated,omitempty"`
}

type GrepResult struct {
	State      string      `json:"state"`
	Pattern    string      `json:"pattern"`
	Path       string      `json:"path"`
	Matches    []GrepMatch `json:"matches"`
	MatchCount int         `json:"matchCount"`
	Truncated  bool        `json:"truncated"`
	Reason     string      `json:"reason,omitempty"`
}

type resolvedGrepRequest struct {
	pattern    string
	path       resolvedPath
	globs      []string
	caseMode   string
	fixed      bool
	maxResults int
}

type ripgrepText struct {
	Text  *string `json:"text,omitempty"`
	Bytes *string `json:"bytes,omitempty"`
}

type ripgrepEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       ripgrepText `json:"path"`
		Lines      ripgrepText `json:"lines"`
		LineNumber int         `json:"line_number"`
		Submatches []struct {
			Match ripgrepText `json:"match"`
			Start int         `json:"start"`
			End   int         `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

func newGrepTool(rootPath string, permission toolpolicy.Permission) (tool.Tool, error) {
	ripgrepPath, err := exec.LookPath("rg")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find rg executable: %w", err)
	}
	access, err := newFilesystemAccess(rootPath, permission.FilesystemScope)
	if err != nil {
		return nil, err
	}
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name:        toolpolicy.ToolGrep,
			Description: "Search file contents with ripgrep and return bounded structured matches. Prefer this over run_command with rg or grep. " + access.Description(),
		},
		func(ctx agent.Context, args GrepArgs) (GrepResult, error) {
			return grepWithPolicy(access, permission, ripgrepPath, ctx, args)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, filesystemDeniedResult(access, "search"))
}

func grepWithPolicy(access filesystemAccess, permission toolpolicy.Permission, ripgrepPath string, ctx agent.Context, args GrepArgs) (GrepResult, error) {
	request, err := resolveGrepRequest(access, args)
	if err != nil {
		return GrepResult{}, err
	}
	if ctx != nil && permission.ConfirmationMode == toolpolicy.ConfirmationAsk && ctx.ToolConfirmation() == nil {
		if err := requestFilesystemConfirmation(ctx, "search", request.path); err != nil {
			return GrepResult{}, err
		}
		return grepResult(request, "approval_required"), nil
	}
	if ctx != nil && ctx.ToolConfirmation() != nil && !ctx.ToolConfirmation().Confirmed {
		result := grepResult(request, "denied")
		result.Reason = approvalReason(ctx.ToolConfirmation())
		return result, nil
	}
	runContext := context.Background()
	if ctx != nil {
		runContext = ctx
	}
	return executeGrep(runContext, ripgrepPath, access, request)
}

func resolveGrepRequest(access filesystemAccess, args GrepArgs) (resolvedGrepRequest, error) {
	if strings.TrimSpace(args.Pattern) == "" {
		return resolvedGrepRequest{}, fmt.Errorf("pattern is required")
	}
	if len(args.Pattern) > maxGrepPatternBytes || strings.ContainsRune(args.Pattern, 0) {
		return resolvedGrepRequest{}, fmt.Errorf("pattern must be valid text no longer than %d bytes", maxGrepPatternBytes)
	}
	resolved, err := access.Resolve(args.Path)
	if err != nil {
		return resolvedGrepRequest{}, err
	}
	resolved, err = resolveExistingGrepPath(access, resolved)
	if err != nil {
		return resolvedGrepRequest{}, err
	}
	caseMode := strings.ToLower(strings.TrimSpace(args.CaseMode))
	if caseMode == "" {
		caseMode = "smart"
	}
	if caseMode != "smart" && caseMode != "sensitive" && caseMode != "insensitive" {
		return resolvedGrepRequest{}, fmt.Errorf("caseMode must be smart, sensitive, or insensitive")
	}
	if len(args.Globs) > maxGrepGlobs {
		return resolvedGrepRequest{}, fmt.Errorf("at most %d globs are supported", maxGrepGlobs)
	}
	globs := make([]string, 0, len(args.Globs))
	for _, glob := range args.Globs {
		if strings.TrimSpace(glob) == "" || len(glob) > maxGrepGlobBytes || strings.ContainsRune(glob, 0) {
			return resolvedGrepRequest{}, fmt.Errorf("each glob must be non-empty and at most %d bytes", maxGrepGlobBytes)
		}
		globs = append(globs, glob)
	}
	maxResults := args.MaxResults
	if maxResults == 0 {
		maxResults = defaultGrepMaxResults
	}
	if maxResults < 1 || maxResults > maxGrepResults {
		return resolvedGrepRequest{}, fmt.Errorf("maxResults must be between 1 and %d", maxGrepResults)
	}
	return resolvedGrepRequest{
		pattern:    args.Pattern,
		path:       resolved,
		globs:      globs,
		caseMode:   caseMode,
		fixed:      args.FixedStrings,
		maxResults: maxResults,
	}, nil
}

func resolveExistingGrepPath(access filesystemAccess, resolved resolvedPath) (resolvedPath, error) {
	realPath, err := filepath.EvalSymlinks(resolved.Absolute)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("resolve search path %q: %w", resolved.Display, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("inspect search path %q: %w", resolved.Display, err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return resolvedPath{}, fmt.Errorf("search path %q is not a file or directory", resolved.Display)
	}
	if access.scope != toolpolicy.ScopeComputer {
		realBoundary, err := filepath.EvalSymlinks(access.boundaryRoot)
		if err != nil {
			return resolvedPath{}, fmt.Errorf("resolve search boundary: %w", err)
		}
		relative, err := filepath.Rel(realBoundary, realPath)
		if err != nil || pathEscapes(relative) {
			return resolvedPath{}, fmt.Errorf("%w: %q", errPathOutsideScope, resolved.Display)
		}
	}
	resolved.Absolute = filepath.Clean(realPath)
	return resolved, nil
}

func executeGrep(ctx context.Context, ripgrepPath string, access filesystemAccess, request resolvedGrepRequest) (GrepResult, error) {
	searchContext, cancel := context.WithTimeout(ctx, defaultGrepTimeout)
	defer cancel()

	arguments := []string{"--json", "--line-number", "--column", "--color=never", "--no-heading", "--with-filename"}
	switch request.caseMode {
	case "smart":
		arguments = append(arguments, "--smart-case")
	case "insensitive":
		arguments = append(arguments, "--ignore-case")
	}
	if request.fixed {
		arguments = append(arguments, "--fixed-strings")
	}
	for _, glob := range request.globs {
		arguments = append(arguments, "--glob", glob)
	}
	arguments = append(arguments, "--", request.pattern, request.path.Absolute)

	command := exec.CommandContext(searchContext, ripgrepPath, arguments...)
	command.Dir = access.workspaceRoot
	command.WaitDelay = time.Second
	configureCommandProcess(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return GrepResult{}, fmt.Errorf("capture rg output: %w", err)
	}
	stderr := newBoundedCommandOutput(nil)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return GrepResult{}, fmt.Errorf("start rg: %w", err)
	}

	result := grepResult(request, "no_matches")
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxGrepJSONEventBytes)
	var parseErr error
	for scanner.Scan() {
		match, matched, err := parseRipgrepMatch(scanner.Bytes(), access.workspaceRoot)
		if err != nil {
			parseErr = err
			cancel()
			break
		}
		if !matched {
			continue
		}
		if len(result.Matches) >= request.maxResults {
			result.Truncated = true
			cancel()
			break
		}
		result.Matches = append(result.Matches, match)
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	cleanupCommandProcess(command)
	if err := ctx.Err(); err != nil {
		return GrepResult{}, err
	}
	if errors.Is(searchContext.Err(), context.DeadlineExceeded) {
		return GrepResult{}, fmt.Errorf("rg exceeded its %d second timeout", int(defaultGrepTimeout/time.Second))
	}
	if parseErr != nil {
		return GrepResult{}, parseErr
	}
	if scanErr != nil && !result.Truncated {
		return GrepResult{}, fmt.Errorf("read rg output: %w", scanErr)
	}
	if waitErr != nil && !result.Truncated {
		if exitCode, ok := commandExitCode(waitErr); !ok || exitCode != 1 {
			message, _, _ := stderr.Result()
			if strings.TrimSpace(message) != "" {
				return GrepResult{}, fmt.Errorf("rg: %s", strings.TrimSpace(message))
			}
			return GrepResult{}, fmt.Errorf("run rg: %w", waitErr)
		}
	}
	result.MatchCount = len(result.Matches)
	if result.MatchCount > 0 {
		result.State = "matched"
	}
	return result, nil
}

func parseRipgrepMatch(encoded []byte, workspaceRoot string) (GrepMatch, bool, error) {
	var event ripgrepEvent
	if err := json.Unmarshal(encoded, &event); err != nil {
		return GrepMatch{}, false, fmt.Errorf("decode rg JSON: %w", err)
	}
	if event.Type != "match" {
		return GrepMatch{}, false, nil
	}
	path, err := decodeRipgrepText(event.Data.Path)
	if err != nil {
		return GrepMatch{}, false, fmt.Errorf("decode rg path: %w", err)
	}
	line, err := decodeRipgrepText(event.Data.Lines)
	if err != nil {
		return GrepMatch{}, false, fmt.Errorf("decode rg line: %w", err)
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	matchText := ""
	column := 1
	if len(event.Data.Submatches) > 0 {
		submatch := event.Data.Submatches[0]
		matchText, err = decodeRipgrepText(submatch.Match)
		if err != nil {
			return GrepMatch{}, false, fmt.Errorf("decode rg match: %w", err)
		}
		if submatch.Start >= 0 && submatch.Start <= len(line) {
			column = utf8.RuneCountInString(line[:submatch.Start]) + 1
		}
	}
	line, textTruncated := truncateUTF8Bytes(line, maxGrepTextBytes)
	matchText, _ = truncateUTF8Bytes(matchText, maxGrepMatchBytes)
	return GrepMatch{
		Path:          displayGrepPath(workspaceRoot, path),
		Line:          event.Data.LineNumber,
		Column:        column,
		Text:          line,
		Match:         matchText,
		TextTruncated: textTruncated,
	}, true, nil
}

func decodeRipgrepText(value ripgrepText) (string, error) {
	if value.Text != nil {
		return *value.Text, nil
	}
	if value.Bytes == nil {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(*value.Bytes)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(decoded), ""), nil
}

func displayGrepPath(workspaceRoot, path string) string {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	relative, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	if relative == "" {
		return "."
	}
	return filepath.ToSlash(relative)
}

func truncateUTF8Bytes(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

func grepResult(request resolvedGrepRequest, state string) GrepResult {
	return GrepResult{
		State:   state,
		Pattern: request.pattern,
		Path:    request.path.Display,
		Matches: []GrepMatch{},
	}
}
