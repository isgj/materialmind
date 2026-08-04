package toolpolicy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNormalizePermissionsRequiresCompleteValidConfiguration(t *testing.T) {
	permissions := DefaultPermissions()
	fetch, _ := PermissionFor(permissions, ToolFetchURL)
	fetch.TargetRules = []TargetRule{
		{Matcher: TargetOrigin, Target: "HTTPS://Example.COM:443/docs", ConfirmationMode: ConfirmationAllow},
		{Matcher: TargetExactURL, Target: "https://example.com/docs#section", ConfirmationMode: ConfirmationAllow},
	}
	for index := range permissions {
		if permissions[index].ToolName == ToolFetchURL {
			permissions[index] = fetch
		}
	}

	normalized, err := NormalizePermissions(permissions)
	if err != nil {
		t.Fatalf("NormalizePermissions() error = %v", err)
	}
	fetch, _ = PermissionFor(normalized, ToolFetchURL)
	if len(fetch.TargetRules) != 2 || fetch.TargetRules[0].Target != "https://example.com/docs" || fetch.TargetRules[1].Target != "https://example.com" {
		t.Fatalf("normalized fetch rules = %#v", fetch.TargetRules)
	}

	if _, err := NormalizePermissions(permissions[:len(permissions)-1]); err == nil {
		t.Fatal("NormalizePermissions() missing tool error = nil")
	}
	invalid := DefaultPermissions()
	invalid[0].FilesystemScope = "outside"
	if _, err := NormalizePermissions(invalid); err == nil {
		t.Fatal("NormalizePermissions() invalid scope error = nil")
	}
}

func TestConfirmationForURLUsesMostSpecificRule(t *testing.T) {
	permission := Permission{
		ToolName:         ToolFetchURL,
		ConfirmationMode: ConfirmationAsk,
		TargetRules: []TargetRule{
			{Matcher: TargetOrigin, Target: "https://example.com", ConfirmationMode: ConfirmationAllow},
			{Matcher: TargetExactURL, Target: "https://example.com/private", ConfirmationMode: ConfirmationAsk},
		},
	}

	mode, err := ConfirmationForURL(permission, "https://EXAMPLE.com:443/docs#top")
	if err != nil || mode != ConfirmationAllow {
		t.Fatalf("ConfirmationForURL(origin) = %q, %v", mode, err)
	}
	mode, err = ConfirmationForURL(permission, "https://example.com/private")
	if err != nil || mode != ConfirmationAsk {
		t.Fatalf("ConfirmationForURL(exact) = %q, %v", mode, err)
	}
	mode, err = ConfirmationForURL(permission, "https://other.example/docs")
	if err != nil || mode != ConfirmationAsk {
		t.Fatalf("ConfirmationForURL(default) = %q, %v", mode, err)
	}
}

func TestFindRepositoryRootUsesNearestMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	nestedRepository := filepath.Join(root, "packages", "nested")
	workspace := filepath.Join(nestedRepository, "src")
	if err := os.MkdirAll(filepath.Join(nestedRepository, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}

	found, ok := FindRepositoryRoot(workspace)
	if !ok || found != nestedRepository {
		t.Fatalf("FindRepositoryRoot() = %q, %v, want %q", found, ok, nestedRepository)
	}
}

func TestRunCommandDefaultsToApprovalAndWorkspaceDirectoryScope(t *testing.T) {
	definition, ok := DefinitionFor(ToolRunCommand)
	if !ok {
		t.Fatal("DefinitionFor(run_command) ok = false")
	}
	if definition.DefaultConfirmation != ConfirmationAsk || definition.DefaultFilesystemScope != ScopeWorkspace {
		t.Fatalf("run_command definition = %#v", definition)
	}
	if !slices.Equal(definition.SupportedScopes, []FilesystemScope{ScopeWorkspace, ScopeRepository}) {
		t.Fatalf("run_command scopes = %#v", definition.SupportedScopes)
	}
}

func TestGrepDefaultsToUnattendedWorkspaceScope(t *testing.T) {
	definition, ok := DefinitionFor(ToolGrep)
	if !ok {
		t.Fatal("DefinitionFor(grep) ok = false")
	}
	if definition.DefaultConfirmation != ConfirmationAllow || definition.DefaultFilesystemScope != ScopeWorkspace {
		t.Fatalf("grep definition = %#v", definition)
	}
	if !slices.Equal(definition.SupportedScopes, filesystemScopes()) {
		t.Fatalf("grep scopes = %#v", definition.SupportedScopes)
	}
}

func TestSessionNotesDefaultToUnattendedWithoutFilesystemScope(t *testing.T) {
	for _, name := range []string{ToolReadSessionNotes, ToolUpdateSessionNotes} {
		definition, ok := DefinitionFor(name)
		if !ok {
			t.Fatalf("DefinitionFor(%s) ok = false", name)
		}
		if definition.DefaultConfirmation != ConfirmationAllow ||
			definition.DefaultFilesystemScope != "" ||
			len(definition.SupportedScopes) != 0 {
			t.Fatalf("%s definition = %#v", name, definition)
		}
	}
}
