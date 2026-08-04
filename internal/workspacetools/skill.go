package workspacetools

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/agentskills"
	"materialmind/internal/toolpolicy"
)

type LoadSkillArgs struct {
	Name     string `json:"name" jsonschema:"Exact skill name from the available-skills catalog."`
	Resource string `json:"resource,omitempty" jsonschema:"Optional relative resource path inside the skill directory. Omit to load SKILL.md instructions."`
}

type LoadSkillResult = agentskills.LoadedResource

type skillConfirmationPayload struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Resource string `json:"resource"`
}

func newLoadSkillTool(catalog agentskills.Catalog, provided ...toolpolicy.Permission) (tool.Tool, error) {
	permission := configuredPermission(toolpolicy.ToolLoadSkill, provided)
	confirmationDescription := "Loads follow the configured skill confirmation policy."
	if permission.ConfirmationMode == toolpolicy.ConfirmationAllow {
		confirmationDescription = "Discovered skills may be loaded without user confirmation."
	}
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name: toolpolicy.ToolLoadSkill,
			Description: "Load a discovered skill's instructions or one of its referenced text resources. " +
				"Use only exact names from the available-skills catalog. " + confirmationDescription,
		},
		func(ctx agent.Context, args LoadSkillArgs) (LoadSkillResult, error) {
			return loadSkillWithPolicy(catalog, permission, ctx, args)
		},
	)
	if err != nil {
		return nil, err
	}
	return newApprovalAwareTool(baseTool, loadSkillDeniedResult(catalog))
}

func loadSkillWithPolicy(catalog agentskills.Catalog, permission toolpolicy.Permission, ctx agent.Context, args LoadSkillArgs) (LoadSkillResult, error) {
	entry, resource, err := catalog.ValidateLoad(args.Name, args.Resource)
	if err != nil {
		return LoadSkillResult{}, err
	}
	var confirmation *toolconfirmation.ToolConfirmation
	if ctx != nil {
		confirmation = ctx.ToolConfirmation()
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk && confirmation == nil {
		if ctx == nil {
			return LoadSkillResult{}, fmt.Errorf("agent context is required to request skill approval")
		}
		if err := requestSkillConfirmation(ctx, entry.Name, resource); err != nil {
			return LoadSkillResult{}, err
		}
		return LoadSkillResult{
			State:       "approval_required",
			Name:        entry.Name,
			Description: entry.Description,
			Source:      entry.Source,
			Resource:    resource,
		}, nil
	}
	if confirmation != nil && !confirmation.Confirmed {
		return deniedSkillResult(entry, resource, approvalReason(confirmation)), nil
	}
	return catalog.Load(entry.Name, resource)
}

func requestSkillConfirmation(ctx agent.Context, name, resource string) error {
	target := fmt.Sprintf("skill %s", name)
	if resource != "SKILL.md" {
		target = fmt.Sprintf("resource %s from skill %s", resource, name)
	}
	if err := ctx.RequestConfirmation(
		"Allow the agent to load "+target+"?",
		skillConfirmationPayload{Kind: "skill_load", Name: name, Resource: resource},
	); err != nil {
		return fmt.Errorf("request skill approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func loadSkillDeniedResult(catalog agentskills.Catalog) deniedResultFunc {
	return func(input map[string]any, confirmation *toolconfirmation.ToolConfirmation) (map[string]any, error) {
		name, _ := input["name"].(string)
		resource, _ := input["resource"].(string)
		entry, resource, err := catalog.ValidateLoad(name, resource)
		if err != nil {
			return nil, err
		}
		result := deniedSkillResult(entry, resource, approvalReason(confirmation))
		return map[string]any{
			"state":       result.State,
			"name":        result.Name,
			"description": result.Description,
			"source":      result.Source,
			"resource":    result.Resource,
			"reason":      result.Reason,
		}, nil
	}
}

func deniedSkillResult(entry agentskills.Entry, resource, reason string) LoadSkillResult {
	return LoadSkillResult{
		State:       "denied",
		Name:        entry.Name,
		Description: entry.Description,
		Source:      entry.Source,
		Resource:    resource,
		Reason:      strings.TrimSpace(reason),
	}
}
