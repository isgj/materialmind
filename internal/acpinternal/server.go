package acpinternal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/workspacetools"
)

const maxBrokerResponseBytes = 1 << 20

func RunServer(ctx context.Context, endpoint, token string) error {
	endpoint = strings.TrimSpace(endpoint)
	token = strings.TrimSpace(token)
	if endpoint == "" || token == "" {
		return fmt.Errorf("ACP internal MCP endpoint and token are required")
	}

	server := newServer(endpoint, token)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve ACP internal MCP: %w", err)
	}
	return nil
}

func newServer(endpoint, token string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    ServerName,
			Version: "development",
		},
		&mcp.ServerOptions{
			Instructions: "Provides session-scoped durable notes through MaterialMind. " +
				"These notes are separate from conversation context and are not loaded automatically.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        ToolReadSessionNotes,
			Title:       "Read session notes",
			Annotations: sessionNotesAnnotations(true),
			Description: "Read the concise durable Markdown notes maintained explicitly for this session. " +
				"Use them only for durable decisions, user constraints, important discoveries, or unresolved questions. " +
				"Do not use session notes as a substitute for the current conversation or working context.",
		},
		func(
			callContext context.Context,
			_ *mcp.CallToolRequest,
			input workspacetools.ReadSessionNotesArgs,
		) (*mcp.CallToolResult, workspacetools.ReadSessionNotesResult, error) {
			output, err := callBroker[workspacetools.ReadSessionNotesResult](
				callContext,
				endpoint,
				token,
				ToolReadSessionNotes,
				input,
			)
			return nil, output, err
		},
	)
	mcp.AddTool(
		server,
		&mcp.Tool{
			Name:        ToolUpdateSessionNotes,
			Title:       "Update session notes",
			Annotations: sessionNotesAnnotations(false),
			Description: "Replace the complete durable Markdown notes for this session. Always call " +
				"read_session_notes first and pass its revision. Keep only concise, durable decisions, user " +
				"constraints, important discoveries, and unresolved questions; revise or remove stale entries. " +
				"Never store private reasoning, routine progress, plans, logs, file contents, or credentials.",
		},
		func(
			callContext context.Context,
			_ *mcp.CallToolRequest,
			input workspacetools.UpdateSessionNotesArgs,
		) (*mcp.CallToolResult, workspacetools.UpdateSessionNotesResult, error) {
			output, err := callBroker[workspacetools.UpdateSessionNotesResult](
				callContext,
				endpoint,
				token,
				ToolUpdateSessionNotes,
				input,
			)
			return nil, output, err
		},
	)

	return server
}

func sessionNotesAnnotations(readOnly bool) *mcp.ToolAnnotations {
	closedWorld := false
	destructive := !readOnly
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  !readOnly,
		OpenWorldHint:   &closedWorld,
	}
}

func callBroker[Output any](
	ctx context.Context,
	endpoint, token, toolName string,
	arguments any,
) (Output, error) {
	var zero Output
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		return zero, fmt.Errorf("encode %s arguments: %w", toolName, err)
	}
	encodedRequest, err := json.Marshal(BrokerRequest{
		ToolName:  toolName,
		Arguments: encodedArguments,
	})
	if err != nil {
		return zero, fmt.Errorf("encode %s broker request: %w", toolName, err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(encodedRequest),
	)
	if err != nil {
		return zero, fmt.Errorf("create %s broker request: %w", toolName, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return zero, fmt.Errorf("call MaterialMind session tool %s: %w", toolName, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBrokerResponseBytes+1))
	if err != nil {
		return zero, fmt.Errorf("read %s broker response: %w", toolName, err)
	}
	if len(body) > maxBrokerResponseBytes {
		return zero, fmt.Errorf("%s broker response exceeds 1 MiB", toolName)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && strings.TrimSpace(failure.Error.Message) != "" {
			message = strings.TrimSpace(failure.Error.Message)
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return zero, fmt.Errorf("MaterialMind session tool %s returned %s: %s", toolName, response.Status, message)
	}
	var envelope BrokerResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return zero, fmt.Errorf("decode %s broker response: %w", toolName, err)
	}
	var output Output
	if err := json.Unmarshal(envelope.Output, &output); err != nil {
		return zero, fmt.Errorf("decode %s output: %w", toolName, err)
	}
	return output, nil
}
