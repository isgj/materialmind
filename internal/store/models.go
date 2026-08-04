package store

import (
	"encoding/json"
	"time"
)

const (
	RuntimeADK = "adk"
	RuntimeACP = "acp"

	LLMAuthNone          = "none"
	LLMAuthBearerEnv     = "bearer_env"
	LLMAuthBearerKeyring = "bearer_keyring"
)

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RootPath  string    `json:"rootPath"`
	Available bool      `json:"available"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type LLMProvider struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	APICompatibility    string    `json:"apiCompatibility"`
	BaseURL             string    `json:"baseUrl,omitempty"`
	AuthType            string    `json:"authType"`
	BearerTokenEnvVar   string    `json:"bearerTokenEnvVar,omitempty"`
	CredentialAvailable bool      `json:"credentialAvailable"`
	CredentialBackend   string    `json:"credentialBackend,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type ACPAgent struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Command         string    `json:"command"`
	Arguments       []string  `json:"arguments"`
	Available       bool      `json:"available"`
	ResolvedCommand string    `json:"resolvedCommand,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"

	MCPAuthNone      = "none"
	MCPAuthBearerEnv = "bearer_env"
	MCPAuthOAuth     = "oauth"

	MCPOAuthClientDynamic       = "dynamic"
	MCPOAuthClientPreRegistered = "pre_registered"

	MCPConfirmationAllow = "allow"
	MCPConfirmationAsk   = "ask"
)

type MCPVariableBinding struct {
	Name        string `json:"name"`
	ValueEnvVar string `json:"valueEnvVar"`
}

type MCPServer struct {
	ID                      string               `json:"id"`
	Name                    string               `json:"name"`
	Transport               string               `json:"transport"`
	Command                 string               `json:"command,omitempty"`
	Arguments               []string             `json:"arguments"`
	Environment             []MCPVariableBinding `json:"environment"`
	URL                     string               `json:"url,omitempty"`
	Headers                 []MCPVariableBinding `json:"headers"`
	AuthType                string               `json:"authType"`
	BearerTokenEnvVar       string               `json:"bearerTokenEnvVar,omitempty"`
	OAuthClientMode         string               `json:"oauthClientMode,omitempty"`
	OAuthClientID           string               `json:"oauthClientId,omitempty"`
	OAuthClientSecretEnvVar string               `json:"oauthClientSecretEnvVar,omitempty"`
	OAuthScopes             []string             `json:"oauthScopes"`
	DefaultEnabled          bool                 `json:"defaultEnabled"`
	DefaultConfirmationMode string               `json:"defaultConfirmationMode"`
	DefaultToolPermissions  []MCPToolPermission  `json:"defaultToolPermissions"`
	Available               bool                 `json:"available"`
	CredentialAvailable     bool                 `json:"credentialAvailable"`
	CreatedAt               time.Time            `json:"createdAt"`
	UpdatedAt               time.Time            `json:"updatedAt"`
}

type MCPToolPermission struct {
	ToolName         string `json:"toolName"`
	ConfirmationMode string `json:"confirmationMode"`
}

type MCPServerAssignment struct {
	Server           MCPServer           `json:"server"`
	Enabled          bool                `json:"enabled"`
	ConfirmationMode string              `json:"confirmationMode"`
	ToolPermissions  []MCPToolPermission `json:"toolPermissions"`
}

type SessionMCPServer struct {
	MCPServer
	ConfirmationMode string              `json:"confirmationMode"`
	ToolPermissions  []MCPToolPermission `json:"toolPermissions"`
}

type MCPOAuthMetadata struct {
	MCPServerID           string    `json:"mcpServerId"`
	Resource              string    `json:"resource"`
	AuthorizationEndpoint string    `json:"authorizationEndpoint"`
	TokenEndpoint         string    `json:"tokenEndpoint"`
	RegistrationEndpoint  string    `json:"registrationEndpoint,omitempty"`
	Scopes                []string  `json:"scopes"`
	ClientID              string    `json:"clientId"`
	TokenAuthMethod       string    `json:"tokenAuthMethod,omitempty"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type GenerationSettings struct {
	ContextWindowTokens int64   `json:"contextWindowTokens"`
	MaxOutputTokens     int64   `json:"maxOutputTokens"`
	ReasoningEffort     *string `json:"reasoningEffort"`
}

type RunGenerationOverrides struct {
	ReasoningEffort *string
}

type LLMModel struct {
	ID            string `json:"id"`
	LLMProviderID string `json:"llmProviderId"`
	Name          string `json:"name"`
	ModelID       string `json:"modelId"`
	GenerationSettings
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AppSession struct {
	ID                 string          `json:"id"`
	WorkspaceID        string          `json:"workspaceId"`
	Title              string          `json:"title"`
	RuntimeType        string          `json:"runtimeType"`
	SelectedLLMModelID *string         `json:"selectedLlmModelId"`
	ACPAgentID         *string         `json:"acpAgentId"`
	ACPSessionID       string          `json:"acpSessionId,omitempty"`
	ACPConfigOptions   json.RawMessage `json:"acpConfigOptions"`
	Status             string          `json:"status"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type SessionNotes struct {
	SessionID string    `json:"sessionId"`
	Content   string    `json:"content"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type RunAttachment struct {
	ID        string    `json:"id"`
	RunID     string    `json:"-"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mimeType"`
	Size      int64     `json:"size"`
	Content   []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

type Run struct {
	ID               string `json:"id"`
	SessionID        string `json:"sessionId"`
	InvocationID     string `json:"invocationId,omitempty"`
	Status           string `json:"status"`
	RuntimeType      string `json:"runtimeType"`
	ACPAgentID       string `json:"acpAgentId,omitempty"`
	ACPAgentName     string `json:"acpAgentName,omitempty"`
	LLMProviderID    string `json:"llmProviderId"`
	LLMProviderName  string `json:"llmProviderName"`
	LLMModelID       string `json:"llmModelId"`
	LLMModelName     string `json:"llmModelName"`
	APICompatibility string `json:"apiCompatibility"`
	ModelID          string `json:"modelId"`
	GenerationSettings
	BaseURL           string          `json:"baseUrl,omitempty"`
	BearerTokenEnvVar string          `json:"bearerTokenEnvVar,omitempty"`
	UserMessage       string          `json:"userMessage"`
	Attachments       []RunAttachment `json:"attachments"`
	Error             string          `json:"error,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	CompletedAt       *time.Time      `json:"completedAt,omitempty"`
}

type TranscriptItem struct {
	ID           string          `json:"id"`
	InvocationID string          `json:"invocationId,omitempty"`
	Kind         string          `json:"kind"`
	Role         string          `json:"role,omitempty"`
	Text         string          `json:"text,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	ToolCallID   string          `json:"toolCallId,omitempty"`
	ToolInput    map[string]any  `json:"toolInput,omitempty"`
	ToolOutput   map[string]any  `json:"toolOutput,omitempty"`
	AgentName    string          `json:"agentName,omitempty"`
	AgentLabel   string          `json:"agentLabel,omitempty"`
	AgentPath    string          `json:"agentPath,omitempty"`
	DelegationID string          `json:"delegationId,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	Attachments  []RunAttachment `json:"attachments,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
}
