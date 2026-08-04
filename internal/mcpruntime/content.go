package mcpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/store"
)

type PromptArgumentSummary struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

type PromptSummary struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Arguments   []PromptArgumentSummary `json:"arguments"`
}

type ResourceSummary struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type ResourceTemplateSummary struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type SessionContentServer struct {
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	Prompts           []PromptSummary           `json:"prompts"`
	Resources         []ResourceSummary         `json:"resources"`
	ResourceTemplates []ResourceTemplateSummary `json:"resourceTemplates"`
	Error             string                    `json:"error,omitempty"`
}

type ResourceContent struct {
	URI      string         `json:"uri"`
	MIMEType string         `json:"mimeType,omitempty"`
	Text     string         `json:"text,omitempty"`
	Blob     []byte         `json:"blob,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type ResourceRead struct {
	ServerID   string            `json:"serverId"`
	ServerName string            `json:"serverName"`
	URI        string            `json:"uri"`
	Contents   []ResourceContent `json:"contents"`
}

type PromptMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type PromptExpansion struct {
	ServerID    string          `json:"serverId"`
	ServerName  string          `json:"serverName"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

func (m *Manager) SessionContent(
	ctx context.Context,
	sessionID, workingDirectory string,
	servers []store.SessionMCPServer,
) []SessionContentServer {
	result := make([]SessionContentServer, 0, len(servers))
	for _, configured := range servers {
		item := SessionContentServer{
			ID:                configured.ID,
			Name:              configured.Name,
			Prompts:           []PromptSummary{},
			Resources:         []ResourceSummary{},
			ResourceTemplates: []ResourceTemplateSummary{},
		}
		connection, err := m.sessionConnection(ctx, sessionID, workingDirectory, configured)
		if err != nil {
			item.Error = err.Error()
			result = append(result, item)
			continue
		}
		initialized := connection.session.InitializeResult()
		if initialized == nil || initialized.Capabilities == nil {
			result = append(result, item)
			continue
		}
		if initialized.Capabilities.Prompts != nil {
			item.Prompts, err = listPromptSummaries(ctx, connection.session)
		}
		if err == nil && initialized.Capabilities.Resources != nil {
			item.Resources, err = listResourceSummaries(ctx, connection.session)
		}
		if err == nil && initialized.Capabilities.Resources != nil {
			item.ResourceTemplates, err = listResourceTemplateSummaries(ctx, connection.session)
		}
		if err != nil {
			item.Error = err.Error()
		}
		result = append(result, item)
	}
	return result
}

func (m *Manager) ReadSessionResource(
	ctx context.Context,
	sessionID, workingDirectory, serverID, uri string,
	servers []store.SessionMCPServer,
) (ResourceRead, error) {
	configured, err := configuredSessionServer(servers, serverID)
	if err != nil {
		return ResourceRead{}, err
	}
	connection, err := m.sessionConnection(ctx, sessionID, workingDirectory, configured)
	if err != nil {
		return ResourceRead{}, err
	}
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ResourceRead{}, fmt.Errorf("%w: MCP resource URI is required", store.ErrInvalidInput)
	}
	response, err := connection.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return ResourceRead{}, fmt.Errorf("read MCP resource %q from %q: %w", uri, configured.Name, err)
	}
	result := ResourceRead{
		ServerID:   configured.ID,
		ServerName: configured.Name,
		URI:        uri,
		Contents:   make([]ResourceContent, 0, len(response.Contents)),
	}
	for _, content := range response.Contents {
		if content == nil {
			continue
		}
		result.Contents = append(result.Contents, ResourceContent{
			URI:      content.URI,
			MIMEType: content.MIMEType,
			Text:     content.Text,
			Blob:     content.Blob,
			Meta:     content.Meta,
		})
	}
	return result, nil
}

func (m *Manager) GetSessionPrompt(
	ctx context.Context,
	sessionID, workingDirectory, serverID, name string,
	arguments map[string]string,
	servers []store.SessionMCPServer,
) (PromptExpansion, error) {
	configured, err := configuredSessionServer(servers, serverID)
	if err != nil {
		return PromptExpansion{}, err
	}
	connection, err := m.sessionConnection(ctx, sessionID, workingDirectory, configured)
	if err != nil {
		return PromptExpansion{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return PromptExpansion{}, fmt.Errorf("%w: MCP prompt name is required", store.ErrInvalidInput)
	}
	response, err := connection.session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return PromptExpansion{}, fmt.Errorf("get MCP prompt %q from %q: %w", name, configured.Name, err)
	}
	result := PromptExpansion{
		ServerID:    configured.ID,
		ServerName:  configured.Name,
		Name:        name,
		Description: response.Description,
		Messages:    make([]PromptMessage, 0, len(response.Messages)),
	}
	for _, message := range response.Messages {
		if message == nil || message.Content == nil {
			continue
		}
		encoded, err := message.Content.MarshalJSON()
		if err != nil {
			return PromptExpansion{}, fmt.Errorf("encode MCP prompt content: %w", err)
		}
		var content any
		if err := json.Unmarshal(encoded, &content); err != nil {
			return PromptExpansion{}, fmt.Errorf("decode MCP prompt content: %w", err)
		}
		result.Messages = append(result.Messages, PromptMessage{
			Role:    string(message.Role),
			Content: content,
		})
	}
	return result, nil
}

func (m *Manager) sessionConnection(
	ctx context.Context,
	sessionID, workingDirectory string,
	server store.SessionMCPServer,
) (*connection, error) {
	return m.connection(
		ctx,
		"session:"+sessionID+":"+server.ID,
		workingDirectory,
		server.MCPServer,
		nil,
	)
}

func configuredSessionServer(
	servers []store.SessionMCPServer,
	serverID string,
) (store.SessionMCPServer, error) {
	serverID = strings.TrimSpace(serverID)
	for _, configured := range servers {
		if configured.ID == serverID {
			return configured, nil
		}
	}
	return store.SessionMCPServer{}, fmt.Errorf(
		"%w: MCP server is not enabled for this session",
		store.ErrInvalidInput,
	)
}

func listPromptSummaries(ctx context.Context, session *mcp.ClientSession) ([]PromptSummary, error) {
	result := make([]PromptSummary, 0)
	for prompt, err := range session.Prompts(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP prompts: %w", err)
		}
		arguments := make([]PromptArgumentSummary, 0, len(prompt.Arguments))
		for _, argument := range prompt.Arguments {
			if argument == nil {
				continue
			}
			arguments = append(arguments, PromptArgumentSummary{
				Name:        argument.Name,
				Title:       argument.Title,
				Description: argument.Description,
				Required:    argument.Required,
			})
		}
		result = append(result, PromptSummary{
			Name:        prompt.Name,
			Title:       prompt.Title,
			Description: prompt.Description,
			Arguments:   arguments,
		})
	}
	slices.SortFunc(result, func(left, right PromptSummary) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	return result, nil
}

func listResourceSummaries(ctx context.Context, session *mcp.ClientSession) ([]ResourceSummary, error) {
	result := make([]ResourceSummary, 0)
	for resource, err := range session.Resources(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP resources: %w", err)
		}
		result = append(result, ResourceSummary{
			URI:         resource.URI,
			Name:        resource.Name,
			Title:       resource.Title,
			Description: resource.Description,
			MIMEType:    resource.MIMEType,
			Size:        resource.Size,
		})
	}
	slices.SortFunc(result, func(left, right ResourceSummary) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	return result, nil
}

func listResourceTemplateSummaries(
	ctx context.Context,
	session *mcp.ClientSession,
) ([]ResourceTemplateSummary, error) {
	result := make([]ResourceTemplateSummary, 0)
	for resource, err := range session.ResourceTemplates(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP resource templates: %w", err)
		}
		result = append(result, ResourceTemplateSummary{
			URITemplate: resource.URITemplate,
			Name:        resource.Name,
			Title:       resource.Title,
			Description: resource.Description,
			MIMEType:    resource.MIMEType,
		})
	}
	slices.SortFunc(result, func(left, right ResourceTemplateSummary) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	return result, nil
}
