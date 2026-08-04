package workspacetools

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/agentskills"
	"materialmind/internal/toolpolicy"
)

func TestLoadSkillReturnsInstructionsAndResources(t *testing.T) {
	catalog := testSkillCatalog(t)
	result, err := loadSkillWithPolicy(catalog, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolLoadSkill,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
	}, nil, LoadSkillArgs{Name: "review"})
	if err != nil {
		t.Fatalf("loadSkillWithPolicy() error = %v", err)
	}
	if result.State != "loaded" || result.Content != "Use references/checklist.md." ||
		len(result.AvailableResources) != 1 || result.AvailableResources[0] != "references/checklist.md" {
		t.Fatalf("loadSkillWithPolicy() = %#v", result)
	}
	resource, err := loadSkillWithPolicy(catalog, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolLoadSkill,
		ConfirmationMode: toolpolicy.ConfirmationAllow,
	}, nil, LoadSkillArgs{Name: "review", Resource: "references/checklist.md"})
	if err != nil || resource.Content != "# Checklist\n" {
		t.Fatalf("loadSkillWithPolicy(resource) = %#v, %v", resource, err)
	}
}

func TestLoadSkillRequestsApprovalAfterValidatingTarget(t *testing.T) {
	catalog := testSkillCatalog(t)
	permission := toolpolicy.Permission{
		ToolName:         toolpolicy.ToolLoadSkill,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
	}
	ctx := &fetchTestContext{}
	result, err := loadSkillWithPolicy(catalog, permission, ctx, LoadSkillArgs{Name: "review"})
	if err != nil || result.State != "approval_required" {
		t.Fatalf("loadSkillWithPolicy() = %#v, %v", result, err)
	}
	payload, ok := ctx.payload.(skillConfirmationPayload)
	if !ok || payload.Kind != "skill_load" || payload.Name != "review" || payload.Resource != "SKILL.md" {
		t.Fatalf("skill approval payload = %#v", ctx.payload)
	}
	if !ctx.actions.SkipSummarization {
		t.Fatal("loadSkillWithPolicy() did not skip summarization")
	}

	invalidContext := &fetchTestContext{}
	if _, err := loadSkillWithPolicy(catalog, permission, invalidContext, LoadSkillArgs{
		Name: "review", Resource: "../../secret",
	}); err == nil {
		t.Fatal("loadSkillWithPolicy(traversal) error = nil")
	}
	if invalidContext.payload != nil {
		t.Fatalf("invalid resource requested approval: %#v", invalidContext.payload)
	}
}

func TestLoadSkillReturnsDenialReason(t *testing.T) {
	catalog := testSkillCatalog(t)
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: false,
		Payload:   map[string]any{"reason": "Do not load project instructions."},
	}}
	result, err := loadSkillWithPolicy(catalog, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolLoadSkill,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
	}, ctx, LoadSkillArgs{Name: "review"})
	if err != nil || result.State != "denied" || result.Reason != "Do not load project instructions." {
		t.Fatalf("loadSkillWithPolicy() = %#v, %v", result, err)
	}
}

func testSkillCatalog(t *testing.T) agentskills.Catalog {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	skillDirectory := filepath.Join(workspace, ".agents", "skills", "review")
	if err := os.MkdirAll(filepath.Join(skillDirectory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: review\ndescription: Review code carefully\n---\n\nUse references/checklist.md.\n"
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "references", "checklist.md"), []byte("# Checklist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentskills.Discover(workspace)
	if err != nil {
		t.Fatalf("agentskills.Discover() error = %v", err)
	}
	return catalog
}
