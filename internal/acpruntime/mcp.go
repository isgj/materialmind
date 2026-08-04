package acpruntime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/acpinternal"
	"materialmind/internal/store"
)

func configuredMCPServers(
	initialize acp.InitializeResponse,
	servers []store.SessionMCPServer,
	bridge MCPBridgeOptions,
	internal InternalMCPOptions,
	internalToken string,
) ([]acp.McpServer, error) {
	result := make([]acp.McpServer, 0, len(servers)+1)
	internalServer, configured, err := internalMCPServer(internal, internalToken)
	if err != nil {
		return nil, err
	}
	if configured {
		result = append(result, internalServer)
	}
	for _, server := range servers {
		switch server.Transport {
		case store.MCPTransportStdio:
			command, err := exec.LookPath(server.Command)
			if err != nil {
				return nil, fmt.Errorf(
					"MCP command %q for %q is unavailable: %w",
					server.Command,
					server.Name,
					err,
				)
			}
			environment := make([]acp.EnvVariable, 0, len(server.Environment))
			for _, binding := range server.Environment {
				value, ok := os.LookupEnv(binding.ValueEnvVar)
				if !ok {
					return nil, fmt.Errorf(
						"environment variable %s required by MCP server %q is not set",
						binding.ValueEnvVar,
						server.Name,
					)
				}
				environment = append(environment, acp.EnvVariable{
					Name:  binding.Name,
					Value: value,
				})
			}
			result = append(result, acp.McpServer{
				Stdio: &acp.McpServerStdio{
					Name:    server.Name,
					Command: command,
					Args:    append([]string{}, server.Arguments...),
					Env:     environment,
				},
			})
		case store.MCPTransportHTTP:
			if server.AuthType == store.MCPAuthOAuth {
				bridged, err := oauthBridgeServer(server.MCPServer, bridge)
				if err != nil {
					return nil, err
				}
				result = append(result, bridged)
				continue
			}
			if !initialize.AgentCapabilities.McpCapabilities.Http {
				return nil, fmt.Errorf(
					"%w: ACP agent does not support HTTP MCP servers required by %q",
					ErrClientCapabilityUnsupported,
					server.Name,
				)
			}
			headers := make([]acp.HttpHeader, 0, len(server.Headers)+1)
			for _, binding := range server.Headers {
				value, ok := os.LookupEnv(binding.ValueEnvVar)
				if !ok {
					return nil, fmt.Errorf(
						"environment variable %s required by MCP server %q is not set",
						binding.ValueEnvVar,
						server.Name,
					)
				}
				headers = append(headers, acp.HttpHeader{
					Name:  binding.Name,
					Value: value,
				})
			}
			if server.AuthType == store.MCPAuthBearerEnv {
				token := strings.TrimSpace(os.Getenv(server.BearerTokenEnvVar))
				if token == "" {
					return nil, fmt.Errorf(
						"environment variable %s required by MCP server %q is not set",
						server.BearerTokenEnvVar,
						server.Name,
					)
				}
				headers = append(headers, acp.HttpHeader{
					Name:  "Authorization",
					Value: "Bearer " + token,
				})
			}
			result = append(result, acp.McpServer{
				Http: &acp.McpServerHttpInline{
					Type:    "http",
					Name:    server.Name,
					Url:     server.URL,
					Headers: headers,
				},
			})
		default:
			return nil, fmt.Errorf(
				"unsupported MCP transport %q for %q",
				server.Transport,
				server.Name,
			)
		}
	}
	return result, nil
}

func internalMCPServer(
	options InternalMCPOptions,
	token string,
) (acp.McpServer, bool, error) {
	command := strings.TrimSpace(options.Command)
	endpoint := strings.TrimSpace(options.Endpoint)
	token = strings.TrimSpace(token)
	if command == "" && endpoint == "" && token == "" {
		return acp.McpServer{}, false, nil
	}
	if command == "" || endpoint == "" || token == "" {
		return acp.McpServer{}, false, fmt.Errorf(
			"%w: MaterialMind session notes MCP is not fully configured",
			ErrClientCapabilityUnsupported,
		)
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return acp.McpServer{}, false, fmt.Errorf(
			"MaterialMind session notes MCP command %q is unavailable: %w",
			command,
			err,
		)
	}
	return acp.McpServer{
		Stdio: &acp.McpServerStdio{
			Name:    acpinternal.ServerName,
			Command: resolved,
			Args:    []string{"acp-session-mcp"},
			Env: []acp.EnvVariable{
				{Name: acpinternal.EndpointEnvironment, Value: endpoint},
				{Name: acpinternal.TokenEnvironment, Value: token},
			},
		},
	}, true, nil
}

func oauthBridgeServer(
	server store.MCPServer,
	bridge MCPBridgeOptions,
) (acp.McpServer, error) {
	command := strings.TrimSpace(bridge.Command)
	if command == "" || strings.TrimSpace(bridge.DatabasePath) == "" {
		return acp.McpServer{}, fmt.Errorf(
			"%w: MaterialMind MCP bridge is unavailable for OAuth server %q",
			ErrClientCapabilityUnsupported,
			server.Name,
		)
	}
	credentialStoreMode := bridge.CredentialStoreMode
	if bridge.CredentialStoreBackend != nil {
		switch backend := bridge.CredentialStoreBackend(); backend {
		case "memory":
			return acp.McpServer{}, fmt.Errorf(
				"%w: OAuth MCP server %q requires the OS keyring for an ACP session; the current credential store is memory-only",
				ErrClientCapabilityUnsupported,
				server.Name,
			)
		case "os_keyring":
			credentialStoreMode = "keyring"
		}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return acp.McpServer{}, fmt.Errorf(
			"MaterialMind MCP bridge command %q is unavailable: %w",
			command,
			err,
		)
	}
	encoded, err := json.Marshal(server)
	if err != nil {
		return acp.McpServer{}, fmt.Errorf("encode MCP bridge configuration: %w", err)
	}
	arguments := []string{
		"mcp-bridge",
		"--database",
		bridge.DatabasePath,
		"--server",
		base64.RawURLEncoding.EncodeToString(encoded),
	}
	if credentialStoreMode != "" {
		arguments = append(
			arguments,
			"--credential-store",
			credentialStoreMode,
		)
	}
	return acp.McpServer{
		Stdio: &acp.McpServerStdio{
			Name:    server.Name,
			Command: resolved,
			Args:    arguments,
			Env:     []acp.EnvVariable{},
		},
	}, nil
}
