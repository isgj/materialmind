package mcpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/store"
)

func (m *Manager) RunStdioBridge(
	ctx context.Context,
	remote store.MCPServer,
) error {
	connection, err := m.connection(
		ctx,
		"bridge:"+remote.ID,
		"",
		remote,
		nil,
	)
	if err != nil {
		return fmt.Errorf("connect bridged MCP server %q: %w", remote.Name, err)
	}
	tools, err := listTools(ctx, connection.session)
	if err != nil {
		return fmt.Errorf("list bridged MCP tools from %q: %w", remote.Name, err)
	}
	bridge := mcp.NewServer(
		&mcp.Implementation{
			Name:    "MaterialMind MCP bridge",
			Version: "development",
		},
		&mcp.ServerOptions{
			Instructions: "Forwards MCP tool calls to " + remote.Name + ".",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	for _, remoteTool := range tools {
		toolDefinition := remoteTool
		inputSchema, err := bridgeInputSchema(toolDefinition.InputSchema)
		if err != nil {
			return fmt.Errorf(
				"normalize bridged schema for %s: %w",
				toolDefinition.Name,
				err,
			)
		}
		bridge.AddTool(
			&mcp.Tool{
				Name:        toolDefinition.Name,
				Description: toolDefinition.Description,
				Annotations: toolDefinition.Annotations,
				InputSchema: inputSchema,
			},
			func(
				callContext context.Context,
				request *mcp.CallToolRequest,
			) (*mcp.CallToolResult, error) {
				return connection.session.CallTool(
					callContext,
					&mcp.CallToolParams{
						Name:      toolDefinition.Name,
						Arguments: request.Params.Arguments,
					},
				)
			},
		)
	}
	if err := bridge.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve MCP stdio bridge for %q: %w", remote.Name, err)
	}
	return nil
}

func bridgeInputSchema(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	normalized, ok := normalizeBridgeSchema(schema).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input schema is not an object")
	}
	if rawType, exists := normalized["type"]; exists {
		if schemaType, ok := rawType.(string); !ok || schemaType != "object" {
			return nil, fmt.Errorf("root schema must have object type")
		}
	} else {
		normalized["type"] = "object"
	}
	if _, exists := normalized["properties"]; !exists {
		normalized["properties"] = map[string]any{}
	}
	return normalized, nil
}

func normalizeBridgeSchema(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			if key == "type" {
				switch typed := child.(type) {
				case string:
					result[key] = strings.ToLower(typed)
				case []any:
					types := make([]any, len(typed))
					for index, item := range typed {
						if text, ok := item.(string); ok {
							types[index] = strings.ToLower(text)
						} else {
							types[index] = item
						}
					}
					result[key] = types
				default:
					result[key] = child
				}
				continue
			}
			result[key] = normalizeBridgeSchema(child)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			result[index] = normalizeBridgeSchema(child)
		}
		return result
	default:
		return value
	}
}
