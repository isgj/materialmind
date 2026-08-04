package acpinternal

import "encoding/json"

const (
	ServerName = "MaterialMind session notes"

	ToolReadSessionNotes   = "read_session_notes"
	ToolUpdateSessionNotes = "update_session_notes"

	EndpointEnvironment = "MATERIALMIND_ACP_INTERNAL_MCP_ENDPOINT"
	TokenEnvironment    = "MATERIALMIND_ACP_INTERNAL_MCP_TOKEN"
)

type BrokerRequest struct {
	ToolName  string          `json:"toolName"`
	Arguments json.RawMessage `json:"arguments"`
}

type BrokerResponse struct {
	Output json.RawMessage `json:"output"`
}
