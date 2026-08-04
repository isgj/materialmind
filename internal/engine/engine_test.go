package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"materialmind/internal/agentskills"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
)

func TestAgentInstructionIncludesDiscoveredSkillCatalog(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	workspaceRoot := t.TempDir()
	skillDirectory := filepath.Join(workspaceRoot, ".agents", "skills", "review")
	if err := os.MkdirAll(skillDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: review\ndescription: Review changes carefully\n---\n\nInspect every change.\n"
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentskills.Discover(workspaceRoot)
	if err != nil {
		t.Fatalf("agentskills.Discover() error = %v", err)
	}

	instruction := agentInstruction(
		store.Workspace{RootPath: workspaceRoot},
		toolpolicy.DefaultPermissions(),
		catalog,
		nil,
	)
	if !strings.Contains(instruction, "review [workspace]: Review changes carefully") ||
		!strings.Contains(instruction, "call load_skill") {
		t.Fatalf("agentInstruction() = %q", instruction)
	}
}

func TestBuiltInSubAgentProfilesExposeExpectedTools(t *testing.T) {
	profiles := builtInSubAgentProfiles()
	if len(profiles) != 7 {
		t.Fatalf("builtInSubAgentProfiles() returned %d profiles, want 7", len(profiles))
	}

	profilesByName := make(map[string]subAgentProfile, len(profiles))
	for _, profile := range profiles {
		profilesByName[profile.Name] = profile
		for _, name := range profile.ToolNames {
			switch name {
			case toolpolicy.ToolListDirectory,
				toolpolicy.ToolReadFile,
				toolpolicy.ToolGrep,
				toolpolicy.ToolLoadSkill,
				toolpolicy.ToolRunCommand:
			default:
				t.Fatalf("profile %q exposes unsupported tool %q", profile.Name, name)
			}
		}
	}

	explorer := profilesByName["workspace_explorer"]
	if explorer.InspectionCommands || slices.Contains(explorer.ToolNames, toolpolicy.ToolRunCommand) {
		t.Fatalf("workspace_explorer exposes run_command: %#v", explorer)
	}

	for _, name := range []string{
		"code_reviewer",
		"security_reviewer",
		"performance_reviewer",
		"test_reviewer",
		"style_reviewer",
		"compatibility_reviewer",
	} {
		profile, ok := profilesByName[name]
		if !ok {
			t.Fatalf("missing reviewer profile %q", name)
		}
		if !profile.InspectionCommands || !slices.Contains(profile.ToolNames, toolpolicy.ToolRunCommand) {
			t.Fatalf("reviewer profile %q does not expose inspection commands: %#v", name, profile)
		}
	}
}

func TestAgentInstructionUsesSharedIgnoredReviewArtifact(t *testing.T) {
	instruction := agentInstruction(
		store.Workspace{RootPath: t.TempDir()},
		toolpolicy.DefaultPermissions(),
		agentskills.Catalog{},
		nil,
	)
	for _, expected := range []string{
		".materialmind/review-artifacts/",
		"self-ignored with a `.gitignore` containing `*`",
		"pass the same repository-relative artifact path to every reviewer",
		"instead of reconstructing version-control state",
	} {
		if !strings.Contains(instruction, expected) {
			t.Errorf("agentInstruction() does not contain %q", expected)
		}
	}
}

func TestReviewerInstructionStartsFromPreparedDiff(t *testing.T) {
	profile, ok := subAgentProfileForName("code_reviewer")
	if !ok {
		t.Fatal("code_reviewer profile is missing")
	}
	instruction := subAgentInstruction(
		profile,
		store.Workspace{RootPath: t.TempDir()},
		toolpolicy.DefaultPermissions(),
		agentskills.Catalog{},
	)
	for _, expected := range []string{
		"Read that artifact first",
		"complete changed scope",
		"instead of reconstructing changes from version-control status",
		"run_command only for a targeted, non-mutating check",
	} {
		if !strings.Contains(instruction, expected) {
			t.Errorf("subAgentInstruction() does not contain %q", expected)
		}
	}
	if !strings.Contains(subAgentToolDescription(profile), "path to the prepared review diff") {
		t.Error("reviewer tool description does not require the prepared diff path")
	}
}
