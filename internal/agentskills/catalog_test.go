package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverUsesNearestSkillAndIncludesGlobalSkills(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repository := filepath.Join(root, "repository")
	workspace := filepath.Join(repository, "packages", "app")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(home, ".agents", "skills", "global"), "global", "Global guidance", "global instructions")
	writeSkill(t, filepath.Join(home, ".agents", "skills", "shared"), "shared", "Global shared", "global shared instructions")
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "shared"), "shared", "Repository shared", "repository instructions")
	writeSkill(t, filepath.Join(workspace, ".agents", "skills", "local"), "local", "Local guidance", "local instructions")

	catalog, err := discover(workspace, home)
	if err != nil {
		t.Fatalf("discover() error = %v", err)
	}
	entries := catalog.Entries()
	if len(entries) != 3 || entries[0].Name != "global" || entries[1].Name != "local" || entries[2].Name != "shared" {
		t.Fatalf("Entries() = %#v", entries)
	}
	if entries[0].Source != SourceGlobal || entries[1].Source != SourceWorkspace || entries[2].Source != SourceParent {
		t.Fatalf("skill sources = %#v", entries)
	}
	loaded, err := catalog.Load("shared", "")
	if err != nil || loaded.Content != "repository instructions" {
		t.Fatalf("Load(shared) = %#v, %v", loaded, err)
	}
	if !strings.Contains(catalog.Instruction(), "shared [parent]: Repository shared") {
		t.Fatalf("Instruction() = %q", catalog.Instruction())
	}
}

func TestLoadReturnsResourcesAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	skillDirectory := filepath.Join(workspace, ".agents", "skills", "review")
	writeSkill(t, skillDirectory, "review", "Review code", "Read references/checklist.md")
	if err := os.MkdirAll(filepath.Join(skillDirectory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDirectory, "references", "checklist.md"), []byte("# Checklist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := catalog.Load("review", "")
	if err != nil || len(instructions.AvailableResources) != 1 || instructions.AvailableResources[0] != "references/checklist.md" {
		t.Fatalf("Load(review) = %#v, %v", instructions, err)
	}
	resource, err := catalog.Load("review", "references/checklist.md")
	if err != nil || resource.Content != "# Checklist\n" {
		t.Fatalf("Load(resource) = %#v, %v", resource, err)
	}
	if _, err := catalog.Load("review", "../../../../secret.txt"); err == nil {
		t.Fatal("Load(traversal) error = nil")
	}
}

func TestDiscoverSkipsSkillsDisabledForModelInvocation(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(workspace, ".agents", "skills", "manual")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: manual\ndescription: Manual only\ndisable-model-invocation: true\n---\n\nDo something.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries()) != 0 {
		t.Fatalf("Entries() = %#v", catalog.Entries())
	}
}

func writeSkill(t *testing.T, directory, name, description, instructions string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + instructions + "\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}
