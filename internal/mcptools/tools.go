package mcptools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
)

type Options struct {
	YieldAfterApproval func(agent.Context) bool
}

type confirmationPayload struct {
	Kind               string `json:"kind"`
	ServerID           string `json:"serverId"`
	ServerName         string `json:"serverName"`
	ToolName           string `json:"toolName"`
	NamespacedToolName string `json:"namespacedToolName"`
}

func New(
	manager *mcpruntime.Manager,
	definitions []mcpruntime.ToolDefinition,
	options Options,
) ([]tool.Tool, error) {
	result := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
		schema, err := inputSchema(definition.InputSchema)
		if err != nil {
			return nil, fmt.Errorf(
				"convert input schema for MCP tool %q from %q: %w",
				definition.OriginalName,
				definition.ServerName,
				err,
			)
		}
		current := definition
		base, err := functiontool.New(
			functiontool.Config{
				Name:        current.Name,
				Description: mcpToolDescription(current),
				InputSchema: schema,
			},
			func(ctx agent.Context, arguments map[string]any) (map[string]any, error) {
				if current.ConfirmationMode == store.MCPConfirmationAsk {
					confirmation := ctx.ToolConfirmation()
					switch {
					case confirmation == nil:
						if err := requestConfirmation(ctx, current); err != nil {
							return nil, err
						}
						return nil, fmt.Errorf(
							"MCP tool %q %w",
							current.OriginalName,
							tool.ErrConfirmationRequired,
						)
					case !confirmation.Confirmed:
						return map[string]any{
							"state":  "denied",
							"server": current.ServerName,
							"tool":   current.OriginalName,
							"reason": approvalReason(confirmation),
						}, nil
					default:
						if err := validateConfirmation(confirmation, current); err != nil {
							return nil, err
						}
					}
				}
				callResult, err := manager.CallTool(
					ctx,
					current,
					arguments,
					mcpruntime.CallOptions{ToolCallID: ctx.FunctionCallID()},
				)
				if ctx.ToolConfirmation() != nil &&
					options.YieldAfterApproval != nil &&
					options.YieldAfterApproval(ctx) {
					ctx.Actions().SkipSummarization = true
				}
				return callResult, err
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"create MCP tool %q from %q: %w",
				current.OriginalName,
				current.ServerName,
				err,
			)
		}
		result = append(result, base)
	}
	return result, nil
}

func requestConfirmation(
	ctx agent.Context,
	definition mcpruntime.ToolDefinition,
) error {
	payload := confirmationPayload{
		Kind:               "mcp_tool",
		ServerID:           definition.ServerID,
		ServerName:         definition.ServerName,
		ToolName:           definition.OriginalName,
		NamespacedToolName: definition.Name,
	}
	if err := ctx.RequestConfirmation(
		fmt.Sprintf(
			"Allow %s to run %s?",
			definition.ServerName,
			definition.OriginalName,
		),
		payload,
	); err != nil {
		return fmt.Errorf("request MCP tool approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func validateConfirmation(
	confirmation *toolconfirmation.ToolConfirmation,
	definition mcpruntime.ToolDefinition,
) error {
	payload, err := decodeConfirmation(confirmation)
	if err != nil {
		return err
	}
	if payload.Kind != "mcp_tool" ||
		payload.ServerID != definition.ServerID ||
		payload.ToolName != definition.OriginalName ||
		payload.NamespacedToolName != definition.Name {
		return fmt.Errorf("MCP tool approval does not match the requested call")
	}
	return nil
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

func decodeConfirmation(
	confirmation *toolconfirmation.ToolConfirmation,
) (confirmationPayload, error) {
	if confirmation == nil || confirmation.Payload == nil {
		return confirmationPayload{}, errors.New("MCP tool approval payload is missing")
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return confirmationPayload{}, fmt.Errorf("encode MCP tool approval: %w", err)
	}
	var payload confirmationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return confirmationPayload{}, fmt.Errorf("decode MCP tool approval: %w", err)
	}
	return payload, nil
}

func inputSchema(value any) (*jsonschema.Schema, error) {
	if value == nil {
		return &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{},
		}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(normalizeSchema(raw))
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	if schema.Type == "" && len(schema.Types) == 0 {
		schema.Type = "object"
	}
	if schema.Type != "object" &&
		!slicesContains(schema.Types, "object") {
		return nil, fmt.Errorf("root schema must accept an object")
	}
	if schema.Properties == nil {
		schema.Properties = map[string]*jsonschema.Schema{}
	}
	return &schema, nil
}

func normalizeSchema(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			if key == "type" {
				result[key] = normalizeSchemaType(child)
			} else {
				result[key] = normalizeSchema(child)
			}
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			result[index] = normalizeSchema(child)
		}
		return result
	default:
		return value
	}
}

func normalizeSchemaType(value any) any {
	switch current := value.(type) {
	case string:
		return strings.ToLower(current)
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			if text, ok := item.(string); ok {
				result[index] = strings.ToLower(text)
			} else {
				result[index] = item
			}
		}
		return result
	default:
		return value
	}
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mcpToolDescription(definition mcpruntime.ToolDefinition) string {
	description := strings.TrimSpace(definition.Description)
	if description == "" {
		description = "Run the configured external MCP tool."
	}
	return fmt.Sprintf(
		"%s External MCP server: %s. Original tool name: %s.",
		description,
		definition.ServerName,
		definition.OriginalName,
	)
}
