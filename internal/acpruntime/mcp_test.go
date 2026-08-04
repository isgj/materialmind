package acpruntime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/acpinternal"
	"materialmind/internal/store"
)

func TestConfiguredMCPServersIncludesSessionScopedInternalServer(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	servers, err := configuredMCPServers(
		acp.InitializeResponse{},
		nil,
		MCPBridgeOptions{},
		InternalMCPOptions{
			Command:  executable,
			Endpoint: "http://127.0.0.1:8080/api/internal/acp-session-tools",
		},
		"session-token",
	)
	if err != nil {
		t.Fatalf("configuredMCPServers() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Stdio == nil {
		t.Fatalf("configuredMCPServers() = %#v", servers)
	}
	server := servers[0].Stdio
	if server.Name != acpinternal.ServerName || server.Command != executable ||
		!slices.Equal(server.Args, []string{"acp-session-mcp"}) || len(server.Env) != 2 {
		t.Fatalf("internal MCP descriptor = %#v", server)
	}
	values := make(map[string]string, len(server.Env))
	for _, variable := range server.Env {
		values[variable.Name] = variable.Value
	}
	if values[acpinternal.EndpointEnvironment] != "http://127.0.0.1:8080/api/internal/acp-session-tools" ||
		values[acpinternal.TokenEnvironment] != "session-token" {
		t.Fatalf("internal MCP environment = %#v", values)
	}
}

func TestConfiguredMCPServersBuildsACPDescriptors(t *testing.T) {
	t.Setenv("MCP_CHILD_SOURCE", "child-value")
	t.Setenv("MCP_BEARER_SOURCE", "bearer-value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	initialize := acp.InitializeResponse{
		AgentCapabilities: acp.AgentCapabilities{
			McpCapabilities: acp.McpCapabilities{Http: true},
		},
	}
	servers, err := configuredMCPServers(
		initialize,
		[]store.SessionMCPServer{
			{
				MCPServer: store.MCPServer{
					ID:        "stdio-1",
					Name:      "Local tools",
					Transport: store.MCPTransportStdio,
					Command:   executable,
					Arguments: []string{"--mcp"},
					Environment: []store.MCPVariableBinding{{
						Name:        "CHILD_TOKEN",
						ValueEnvVar: "MCP_CHILD_SOURCE",
					}},
				},
			},
			{
				MCPServer: store.MCPServer{
					ID:                "http-1",
					Name:              "Remote tools",
					Transport:         store.MCPTransportHTTP,
					URL:               "https://mcp.example.test/mcp",
					AuthType:          store.MCPAuthBearerEnv,
					BearerTokenEnvVar: "MCP_BEARER_SOURCE",
				},
			},
		},
		MCPBridgeOptions{},
		InternalMCPOptions{},
		"",
	)
	if err != nil {
		t.Fatalf("configuredMCPServers() error = %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("configuredMCPServers() = %#v", servers)
	}
	if servers[0].Stdio == nil ||
		servers[0].Stdio.Command != executable ||
		!slices.Equal(servers[0].Stdio.Args, []string{"--mcp"}) ||
		len(servers[0].Stdio.Env) != 1 ||
		servers[0].Stdio.Env[0].Name != "CHILD_TOKEN" ||
		servers[0].Stdio.Env[0].Value != "child-value" {
		t.Fatalf("stdio descriptor = %#v", servers[0].Stdio)
	}
	if servers[1].Http == nil ||
		servers[1].Http.Url != "https://mcp.example.test/mcp" ||
		len(servers[1].Http.Headers) != 1 ||
		servers[1].Http.Headers[0].Name != "Authorization" ||
		servers[1].Http.Headers[0].Value != "Bearer bearer-value" {
		t.Fatalf("HTTP descriptor = %#v", servers[1].Http)
	}
}

func TestConfiguredMCPServersBridgesOAuthHTTP(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	remote := store.MCPServer{
		ID:              "oauth-1",
		Name:            "OAuth tools",
		Transport:       store.MCPTransportHTTP,
		URL:             "https://mcp.example.test/mcp",
		AuthType:        store.MCPAuthOAuth,
		OAuthClientMode: store.MCPOAuthClientDynamic,
	}
	servers, err := configuredMCPServers(
		acp.InitializeResponse{},
		[]store.SessionMCPServer{{MCPServer: remote}},
		MCPBridgeOptions{
			Command:             executable,
			DatabasePath:        "/tmp/materialmind.db",
			CredentialStoreMode: "keyring",
		},
		InternalMCPOptions{},
		"",
	)
	if err != nil {
		t.Fatalf("configuredMCPServers() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Stdio == nil {
		t.Fatalf("configuredMCPServers() = %#v", servers)
	}
	arguments := servers[0].Stdio.Args
	serverArgument := slices.Index(arguments, "--server")
	if serverArgument < 0 || serverArgument+1 >= len(arguments) {
		t.Fatalf("bridge arguments = %#v", arguments)
	}
	encoded, err := base64.RawURLEncoding.DecodeString(arguments[serverArgument+1])
	if err != nil {
		t.Fatalf("decode bridge server: %v", err)
	}
	var decoded store.MCPServer
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal bridge server: %v", err)
	}
	if decoded.ID != remote.ID || decoded.URL != remote.URL || decoded.AuthType != store.MCPAuthOAuth {
		t.Fatalf("bridge server = %#v", decoded)
	}
	if !slices.Contains(arguments, "keyring") {
		t.Fatalf("bridge arguments omit credential mode: %#v", arguments)
	}
}

func TestConfiguredMCPServersRejectsMemoryOnlyOAuthBridge(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	_, err = configuredMCPServers(
		acp.InitializeResponse{},
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "oauth-1",
				Name:      "OAuth tools",
				Transport: store.MCPTransportHTTP,
				URL:       "https://mcp.example.test/mcp",
				AuthType:  store.MCPAuthOAuth,
			},
		}},
		MCPBridgeOptions{
			Command:                executable,
			DatabasePath:           "/tmp/materialmind.db",
			CredentialStoreBackend: func() string { return "memory" },
		},
		InternalMCPOptions{},
		"",
	)
	if !errors.Is(err, ErrClientCapabilityUnsupported) {
		t.Fatalf("configuredMCPServers() error = %v, want capability error", err)
	}
}

func TestConfiguredMCPServersRequiresHTTPAgentCapability(t *testing.T) {
	_, err := configuredMCPServers(
		acp.InitializeResponse{},
		[]store.SessionMCPServer{{
			MCPServer: store.MCPServer{
				ID:        "http-1",
				Name:      "Remote tools",
				Transport: store.MCPTransportHTTP,
				URL:       "https://mcp.example.test/mcp",
				AuthType:  store.MCPAuthNone,
			},
		}},
		MCPBridgeOptions{},
		InternalMCPOptions{},
		"",
	)
	if !errors.Is(err, ErrClientCapabilityUnsupported) {
		t.Fatalf("configuredMCPServers() error = %v, want capability error", err)
	}
}
