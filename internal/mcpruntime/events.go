package mcpruntime

const (
	EventCallStarted  = "mcp_call_started"
	EventCallFinished = "mcp_call_finished"
	EventProgress     = "mcp_progress"
	EventLog          = "mcp_log"
	EventToolsChanged = "mcp_tools_changed"
	EventUnavailable  = "mcp_server_unavailable"
)

type EventSink func(Event)

type Event struct {
	Type       string `json:"-"`
	SessionID  string `json:"sessionId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ServerID   string `json:"serverId"`
	ServerName string `json:"serverName"`
	ToolName   string `json:"toolName,omitempty"`
	ToolTitle  string `json:"toolTitle,omitempty"`

	Cancelable bool           `json:"cancelable,omitempty"`
	Message    string         `json:"message,omitempty"`
	Progress   float64        `json:"progress,omitempty"`
	Total      float64        `json:"total,omitempty"`
	Output     map[string]any `json:"output,omitempty"`

	Level  string `json:"level,omitempty"`
	Logger string `json:"logger,omitempty"`
	Data   any    `json:"data,omitempty"`

	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Count   int      `json:"count,omitempty"`
	Error   string   `json:"error,omitempty"`
}
