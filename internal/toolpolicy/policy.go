package toolpolicy

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type ConfirmationMode string

const (
	ConfirmationAllow ConfirmationMode = "allow"
	ConfirmationAsk   ConfirmationMode = "ask"
)

type FilesystemScope string

const (
	ScopeWorkspace  FilesystemScope = "workspace"
	ScopeRepository FilesystemScope = "repository"
	ScopeComputer   FilesystemScope = "computer"
)

type TargetMatcher string

const (
	TargetExactURL TargetMatcher = "exact_url"
	TargetOrigin   TargetMatcher = "origin"
)

const (
	ToolListDirectory      = "list_directory"
	ToolReadFile           = "read_file"
	ToolGrep               = "grep"
	ToolFetchURL           = "fetch_url"
	ToolEditFile           = "edit_file"
	ToolLoadSkill          = "load_skill"
	ToolReadSessionNotes   = "read_session_notes"
	ToolUpdateSessionNotes = "update_session_notes"
	ToolRunCommand         = "run_command"
)

type Definition struct {
	Name                    string            `json:"name"`
	Label                   string            `json:"label"`
	Description             string            `json:"description"`
	DefaultConfirmation     ConfirmationMode  `json:"defaultConfirmation"`
	DefaultFilesystemScope  FilesystemScope   `json:"defaultFilesystemScope,omitempty"`
	SupportedScopes         []FilesystemScope `json:"supportedScopes"`
	SupportedTargetMatchers []TargetMatcher   `json:"supportedTargetMatchers"`
}

type TargetRule struct {
	Matcher          TargetMatcher    `json:"matcher"`
	Target           string           `json:"target"`
	ConfirmationMode ConfirmationMode `json:"confirmationMode"`
}

type Permission struct {
	ToolName         string           `json:"toolName"`
	ConfirmationMode ConfirmationMode `json:"confirmationMode"`
	FilesystemScope  FilesystemScope  `json:"filesystemScope"`
	TargetRules      []TargetRule     `json:"targetRules"`
}

var definitions = []Definition{
	{
		Name:                   ToolListDirectory,
		Label:                  "List directory",
		Description:            "Inspect files and directories without reading file contents.",
		DefaultConfirmation:    ConfirmationAllow,
		DefaultFilesystemScope: ScopeWorkspace,
		SupportedScopes:        filesystemScopes(),
	},
	{
		Name:                   ToolReadFile,
		Label:                  "Read file",
		Description:            "Read an inclusive line range from a UTF-8 text file.",
		DefaultConfirmation:    ConfirmationAllow,
		DefaultFilesystemScope: ScopeWorkspace,
		SupportedScopes:        filesystemScopes(),
	},
	{
		Name:                   ToolGrep,
		Label:                  "Search files",
		Description:            "Search file contents with ripgrep and return structured matches.",
		DefaultConfirmation:    ConfirmationAllow,
		DefaultFilesystemScope: ScopeWorkspace,
		SupportedScopes:        filesystemScopes(),
	},
	{
		Name:                    ToolFetchURL,
		Label:                   "Fetch URL",
		Description:             "Fetch bounded text content from public HTTP or HTTPS URLs.",
		DefaultConfirmation:     ConfirmationAsk,
		SupportedScopes:         []FilesystemScope{},
		SupportedTargetMatchers: []TargetMatcher{TargetExactURL, TargetOrigin},
	},
	{
		Name:                   ToolEditFile,
		Label:                  "Edit file",
		Description:            "Create, update, or delete files with a reviewable patch.",
		DefaultConfirmation:    ConfirmationAsk,
		DefaultFilesystemScope: ScopeWorkspace,
		SupportedScopes:        filesystemScopes(),
	},
	{
		Name:                ToolLoadSkill,
		Label:               "Load skill",
		Description:         "Load instructions or resources from a discovered workspace, parent, or global skill.",
		DefaultConfirmation: ConfirmationAllow,
		SupportedScopes:     []FilesystemScope{},
	},
	{
		Name:                ToolReadSessionNotes,
		Label:               "Read session notes",
		Description:         "Read the durable notes maintained explicitly for this session.",
		DefaultConfirmation: ConfirmationAllow,
		SupportedScopes:     []FilesystemScope{},
	},
	{
		Name:                ToolUpdateSessionNotes,
		Label:               "Update session notes",
		Description:         "Replace the durable notes maintained explicitly for this session.",
		DefaultConfirmation: ConfirmationAllow,
		SupportedScopes:     []FilesystemScope{},
	},
	{
		Name:                   ToolRunCommand,
		Label:                  "Run command",
		Description:            "Run a non-interactive command from an allowed workspace or repository directory.",
		DefaultConfirmation:    ConfirmationAsk,
		DefaultFilesystemScope: ScopeWorkspace,
		SupportedScopes:        []FilesystemScope{ScopeWorkspace, ScopeRepository},
	},
}

func Definitions() []Definition {
	result := make([]Definition, len(definitions))
	for index, definition := range definitions {
		result[index] = definition
		result[index].SupportedScopes = append([]FilesystemScope{}, definition.SupportedScopes...)
		result[index].SupportedTargetMatchers = append([]TargetMatcher{}, definition.SupportedTargetMatchers...)
	}
	return result
}

func DefinitionFor(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			result := definition
			result.SupportedScopes = append([]FilesystemScope{}, definition.SupportedScopes...)
			result.SupportedTargetMatchers = append([]TargetMatcher{}, definition.SupportedTargetMatchers...)
			return result, true
		}
	}
	return Definition{}, false
}

func DefaultPermissions() []Permission {
	result := make([]Permission, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, Permission{
			ToolName:         definition.Name,
			ConfirmationMode: definition.DefaultConfirmation,
			FilesystemScope:  definition.DefaultFilesystemScope,
			TargetRules:      []TargetRule{},
		})
	}
	return result
}

func PermissionFor(permissions []Permission, toolName string) (Permission, bool) {
	for _, permission := range permissions {
		if permission.ToolName == toolName {
			permission.TargetRules = slices.Clone(permission.TargetRules)
			return permission, true
		}
	}
	return Permission{}, false
}

func NormalizePermissions(permissions []Permission) ([]Permission, error) {
	if len(permissions) != len(definitions) {
		return nil, fmt.Errorf("permissions must configure all %d tools", len(definitions))
	}
	byName := make(map[string]Permission, len(permissions))
	for _, permission := range permissions {
		permission.ToolName = strings.TrimSpace(permission.ToolName)
		definition, ok := DefinitionFor(permission.ToolName)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", permission.ToolName)
		}
		if _, exists := byName[permission.ToolName]; exists {
			return nil, fmt.Errorf("tool %q is configured more than once", permission.ToolName)
		}
		if !validConfirmationMode(permission.ConfirmationMode) {
			return nil, fmt.Errorf("tool %q has invalid confirmation mode %q", permission.ToolName, permission.ConfirmationMode)
		}
		if len(definition.SupportedScopes) == 0 {
			if permission.FilesystemScope != "" {
				return nil, fmt.Errorf("tool %q does not support filesystem scope", permission.ToolName)
			}
		} else if !slices.Contains(definition.SupportedScopes, permission.FilesystemScope) {
			return nil, fmt.Errorf("tool %q has invalid filesystem scope %q", permission.ToolName, permission.FilesystemScope)
		}

		normalizedRules := make([]TargetRule, 0, len(permission.TargetRules))
		seenRules := make(map[string]struct{}, len(permission.TargetRules))
		for _, rule := range permission.TargetRules {
			if !slices.Contains(definition.SupportedTargetMatchers, rule.Matcher) {
				return nil, fmt.Errorf("tool %q does not support target matcher %q", permission.ToolName, rule.Matcher)
			}
			if !validConfirmationMode(rule.ConfirmationMode) {
				return nil, fmt.Errorf("tool %q target rule has invalid confirmation mode %q", permission.ToolName, rule.ConfirmationMode)
			}
			target, err := NormalizeURLTarget(rule.Matcher, rule.Target)
			if err != nil {
				return nil, fmt.Errorf("tool %q target rule: %w", permission.ToolName, err)
			}
			key := string(rule.Matcher) + "\x00" + target
			if _, exists := seenRules[key]; exists {
				return nil, fmt.Errorf("tool %q has duplicate %s target %q", permission.ToolName, rule.Matcher, target)
			}
			seenRules[key] = struct{}{}
			normalizedRules = append(normalizedRules, TargetRule{
				Matcher:          rule.Matcher,
				Target:           target,
				ConfirmationMode: rule.ConfirmationMode,
			})
		}
		slices.SortFunc(normalizedRules, func(left, right TargetRule) int {
			if left.Matcher != right.Matcher {
				return strings.Compare(string(left.Matcher), string(right.Matcher))
			}
			return strings.Compare(left.Target, right.Target)
		})
		permission.TargetRules = normalizedRules
		byName[permission.ToolName] = permission
	}

	result := make([]Permission, 0, len(definitions))
	for _, definition := range definitions {
		permission, ok := byName[definition.Name]
		if !ok {
			return nil, fmt.Errorf("tool %q is missing", definition.Name)
		}
		result = append(result, permission)
	}
	return result, nil
}

func ConfirmationForURL(permission Permission, rawURL string) (ConfirmationMode, error) {
	exact, err := NormalizeURLTarget(TargetExactURL, rawURL)
	if err != nil {
		return "", err
	}
	origin, err := NormalizeURLTarget(TargetOrigin, rawURL)
	if err != nil {
		return "", err
	}
	for _, rule := range permission.TargetRules {
		if rule.Matcher == TargetExactURL && rule.Target == exact {
			return rule.ConfirmationMode, nil
		}
	}
	for _, rule := range permission.TargetRules {
		if rule.Matcher == TargetOrigin && rule.Target == origin {
			return rule.ConfirmationMode, nil
		}
	}
	return permission.ConfirmationMode, nil
}

func NormalizeURLTarget(matcher TargetMatcher, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("target URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Opaque != "" {
		return "", fmt.Errorf("target must be an absolute HTTP or HTTPS URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("target URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("target URL host is required")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("target URL must not contain credentials")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	parsed.Host = hostname
	if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	}
	if port != "" {
		parsed.Host += ":" + port
	}
	parsed.Fragment = ""

	switch matcher {
	case TargetExactURL:
		return parsed.String(), nil
	case TargetOrigin:
		return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
	default:
		return "", fmt.Errorf("unsupported target matcher %q", matcher)
	}
}

func FindRepositoryRoot(workspaceRoot string) (string, bool) {
	current := filepath.Clean(workspaceRoot)
	for {
		if repositoryMarkerExists(current, ".git") || repositoryMarkerExists(current, ".jj") {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func filesystemScopes() []FilesystemScope {
	return []FilesystemScope{ScopeWorkspace, ScopeRepository, ScopeComputer}
}

func validConfirmationMode(mode ConfirmationMode) bool {
	return mode == ConfirmationAllow || mode == ConfirmationAsk
}

func repositoryMarkerExists(directory, name string) bool {
	_, err := os.Lstat(filepath.Join(directory, name))
	return err == nil
}
