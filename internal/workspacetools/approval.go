package workspacetools

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/toolpolicy"
)

type runnableFunctionTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	ProcessRequest(agent.Context, *model.LLMRequest) error
	Run(agent.Context, any) (map[string]any, error)
}

type deniedResultFunc func(map[string]any, *toolconfirmation.ToolConfirmation) (map[string]any, error)

type approvalAwareTool struct {
	runnableFunctionTool
	deniedResult deniedResultFunc
}

type approvalYieldTool struct {
	runnableFunctionTool
	shouldYield func(agent.Context) bool
}

func newApprovalAwareTool(baseTool tool.Tool, deniedResult deniedResultFunc) (tool.Tool, error) {
	runnable, ok := baseTool.(runnableFunctionTool)
	if !ok {
		return nil, fmt.Errorf("function tool %q is not runnable", baseTool.Name())
	}
	return &approvalAwareTool{runnableFunctionTool: runnable, deniedResult: deniedResult}, nil
}

func newApprovalYieldTool(baseTool tool.Tool, shouldYield func(agent.Context) bool) (tool.Tool, error) {
	runnable, ok := baseTool.(runnableFunctionTool)
	if !ok {
		return nil, fmt.Errorf("function tool %q is not runnable", baseTool.Name())
	}
	return &approvalYieldTool{runnableFunctionTool: runnable, shouldYield: shouldYield}, nil
}

func (t *approvalAwareTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	confirmation := ctx.ToolConfirmation()
	if confirmation == nil || confirmation.Confirmed {
		return t.runnableFunctionTool.Run(ctx, args)
	}
	input, ok := args.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected %s args type, got %T", t.Name(), args)
	}
	return t.deniedResult(input, confirmation)
}

func (t *approvalYieldTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	result, err := t.runnableFunctionTool.Run(ctx, args)
	if ctx.ToolConfirmation() != nil && t.shouldYield(ctx) {
		ctx.Actions().SkipSummarization = true
	}
	return result, err
}

func approvalReason(confirmation *toolconfirmation.ToolConfirmation) string {
	if confirmation == nil || confirmation.Payload == nil {
		return ""
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return ""
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Reason)
}

func configuredPermission(toolName string, provided []toolpolicy.Permission) toolpolicy.Permission {
	if len(provided) > 0 && provided[0].ToolName == toolName {
		return provided[0]
	}
	permission, _ := toolpolicy.PermissionFor(toolpolicy.DefaultPermissions(), toolName)
	return permission
}
