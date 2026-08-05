package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPServerAssignmentsUseLiveDefinitionsForADKSessions(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MCP_SOURCE_TOKEN", "configured")
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	command, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	server, err := dataStore.CreateMCPServer(ctx, MCPServer{
		Name:      "Project tools",
		Transport: MCPTransportStdio,
		Command:   command,
		Arguments: []string{"--mcp-test"},
		Environment: []MCPVariableBinding{{
			Name:        "PROJECT_TOKEN",
			ValueEnvVar: "MCP_SOURCE_TOKEN",
		}},
	})
	if err != nil {
		t.Fatalf("CreateMCPServer() error = %v", err)
	}
	if !server.Available || !server.CredentialAvailable {
		t.Fatalf("CreateMCPServer() availability = %#v", server)
	}
	workspace, err := dataStore.CreateWorkspace(ctx, "Project", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	workspaceAssignments, err := dataStore.GetWorkspaceMCPServers(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() error = %v", err)
	}
	if len(workspaceAssignments) != 1 || workspaceAssignments[0].Enabled {
		t.Fatalf("initial workspace assignments = %#v", workspaceAssignments)
	}

	workspaceAssignments, err = dataStore.ReplaceWorkspaceMCPServers(
		ctx,
		workspace.ID,
		[]MCPServerAssignment{{
			Server:           MCPServer{ID: server.ID},
			Enabled:          true,
			ConfirmationMode: MCPConfirmationAsk,
			ToolPermissions: []MCPToolPermission{{
				ToolName:         "search",
				ConfirmationMode: MCPConfirmationAllow,
			}},
		}},
	)
	if err != nil {
		t.Fatalf("ReplaceWorkspaceMCPServers() error = %v", err)
	}
	if !workspaceAssignments[0].Enabled ||
		workspaceAssignments[0].ConfirmationMode != MCPConfirmationAsk ||
		len(workspaceAssignments[0].ToolPermissions) != 1 {
		t.Fatalf("workspace assignments = %#v", workspaceAssignments)
	}

	sessionRecord, err := dataStore.CreateSession(ctx, workspace.ID, "Review", nil)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionServers, err := dataStore.GetSessionMCPServers(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("GetSessionMCPServers() error = %v", err)
	}
	if len(sessionServers) != 1 ||
		sessionServers[0].Name != "Project tools" ||
		sessionServers[0].ConfirmationMode != MCPConfirmationAsk ||
		len(sessionServers[0].ToolPermissions) != 1 {
		t.Fatalf("inherited session servers = %#v", sessionServers)
	}

	sessionServers, err = dataStore.ReplaceSessionMCPServers(
		ctx,
		sessionRecord.ID,
		[]MCPServerAssignment{{
			Server:           MCPServer{ID: server.ID},
			Enabled:          true,
			ConfirmationMode: MCPConfirmationAllow,
			ToolPermissions: []MCPToolPermission{{
				ToolName:         "search",
				ConfirmationMode: MCPConfirmationAsk,
			}},
		}},
	)
	if err != nil {
		t.Fatalf("ReplaceSessionMCPServers() error = %v", err)
	}
	if sessionServers[0].ConfirmationMode != MCPConfirmationAllow ||
		sessionServers[0].ToolPermissions[0].ConfirmationMode != MCPConfirmationAsk {
		t.Fatalf("updated session servers = %#v", sessionServers)
	}
	workspaceAssignments, err = dataStore.GetWorkspaceMCPServers(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() after session update error = %v", err)
	}
	if workspaceAssignments[0].ConfirmationMode != MCPConfirmationAsk ||
		workspaceAssignments[0].ToolPermissions[0].ConfirmationMode != MCPConfirmationAllow {
		t.Fatalf("workspace changed with session = %#v", workspaceAssignments)
	}
	agentRecord, err := dataStore.CreateACPAgent(
		ctx,
		"Test ACP agent",
		command,
		[]string{"--acp-test"},
	)
	if err != nil {
		t.Fatalf("CreateACPAgent() error = %v", err)
	}
	acpSession, err := dataStore.CreateACPSession(
		ctx,
		workspace.ID,
		"ACP review",
		agentRecord.ID,
	)
	if err != nil {
		t.Fatalf("CreateACPSession() error = %v", err)
	}

	server.Name = "Renamed project tools"
	server.Arguments = []string{"--updated"}
	if _, err := dataStore.UpdateMCPServer(ctx, server.ID, server); err != nil {
		t.Fatalf("UpdateMCPServer() error = %v", err)
	}
	sessionServers, err = dataStore.GetSessionMCPServers(ctx, sessionRecord.ID)
	if err != nil {
		t.Fatalf("GetSessionMCPServers() after server update error = %v", err)
	}
	if sessionServers[0].Name != "Renamed project tools" ||
		len(sessionServers[0].Arguments) != 1 ||
		sessionServers[0].Arguments[0] != "--updated" ||
		sessionServers[0].ConfirmationMode != MCPConfirmationAllow ||
		len(sessionServers[0].ToolPermissions) != 1 ||
		sessionServers[0].ToolPermissions[0].ConfirmationMode != MCPConfirmationAsk {
		t.Fatalf("ADK session did not use live server definition = %#v", sessionServers[0])
	}
	acpServers, err := dataStore.GetSessionMCPServers(ctx, acpSession.ID)
	if err != nil {
		t.Fatalf("GetSessionMCPServers() for ACP session error = %v", err)
	}
	if len(acpServers) != 1 ||
		acpServers[0].Name != "Project tools" ||
		len(acpServers[0].Arguments) != 1 ||
		acpServers[0].Arguments[0] != "--mcp-test" {
		t.Fatalf("ACP session server snapshot changed = %#v", acpServers)
	}

	if _, err := dataStore.ReplaceWorkspaceMCPServers(ctx, workspace.ID, nil); err != nil {
		t.Fatalf("clear workspace MCP servers: %v", err)
	}
	if err := dataStore.DeleteMCPServer(ctx, server.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteMCPServer() with session error = %v, want ErrConflict", err)
	}
	if err := dataStore.DeleteSession(ctx, sessionRecord.ID); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if err := dataStore.DeleteMCPServer(ctx, server.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteMCPServer() with ACP session error = %v, want ErrConflict", err)
	}
	if err := dataStore.DeleteSession(ctx, acpSession.ID); err != nil {
		t.Fatalf("DeleteSession() for ACP session error = %v", err)
	}
	if err := dataStore.DeleteMCPServer(ctx, server.ID); err != nil {
		t.Fatalf("DeleteMCPServer() error = %v", err)
	}
}

func TestMCPServerDefaultsAreSnapshottedPerWorkspace(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	command, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	server, err := dataStore.CreateMCPServer(ctx, MCPServer{
		Name:      "Default project tools",
		Transport: MCPTransportStdio,
		Command:   command,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer() error = %v", err)
	}
	if server.DefaultEnabled ||
		server.DefaultConfirmationMode != MCPConfirmationAsk ||
		len(server.DefaultToolPermissions) != 0 {
		t.Fatalf("initial MCP defaults = %#v", server)
	}

	beforeDefaults, err := dataStore.CreateWorkspace(ctx, "Before defaults", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() before defaults error = %v", err)
	}
	server, err = dataStore.UpdateMCPServerDefaults(
		ctx,
		server.ID,
		true,
		MCPConfirmationAllow,
		[]MCPToolPermission{{
			ToolName:         "create_issue",
			ConfirmationMode: MCPConfirmationAsk,
		}},
	)
	if err != nil {
		t.Fatalf("UpdateMCPServerDefaults() error = %v", err)
	}
	if !server.DefaultEnabled ||
		server.DefaultConfirmationMode != MCPConfirmationAllow ||
		len(server.DefaultToolPermissions) != 1 {
		t.Fatalf("updated MCP defaults = %#v", server)
	}

	beforeAssignments, err := dataStore.GetWorkspaceMCPServers(ctx, beforeDefaults.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() before defaults error = %v", err)
	}
	if len(beforeAssignments) != 1 || beforeAssignments[0].Enabled {
		t.Fatalf("workspace created before defaults changed = %#v", beforeAssignments)
	}

	inheritedWorkspace, err := dataStore.CreateWorkspace(ctx, "Inherited defaults", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() with defaults error = %v", err)
	}
	inheritedAssignments, err := dataStore.GetWorkspaceMCPServers(ctx, inheritedWorkspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() with defaults error = %v", err)
	}
	if len(inheritedAssignments) != 1 ||
		!inheritedAssignments[0].Enabled ||
		inheritedAssignments[0].ConfirmationMode != MCPConfirmationAllow ||
		len(inheritedAssignments[0].ToolPermissions) != 1 ||
		inheritedAssignments[0].ToolPermissions[0].ToolName != "create_issue" ||
		inheritedAssignments[0].ToolPermissions[0].ConfirmationMode != MCPConfirmationAsk {
		t.Fatalf("inherited workspace MCP assignments = %#v", inheritedAssignments)
	}

	if _, err := dataStore.UpdateMCPServerDefaults(
		ctx,
		server.ID,
		false,
		MCPConfirmationAsk,
		nil,
	); err != nil {
		t.Fatalf("disable MCP defaults error = %v", err)
	}
	inheritedAssignments, err = dataStore.GetWorkspaceMCPServers(ctx, inheritedWorkspace.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() after default update error = %v", err)
	}
	if !inheritedAssignments[0].Enabled ||
		inheritedAssignments[0].ConfirmationMode != MCPConfirmationAllow ||
		len(inheritedAssignments[0].ToolPermissions) != 1 {
		t.Fatalf("existing workspace changed with global defaults = %#v", inheritedAssignments)
	}

	afterDefaults, err := dataStore.CreateWorkspace(ctx, "After defaults", t.TempDir())
	if err != nil {
		t.Fatalf("CreateWorkspace() after disabling defaults error = %v", err)
	}
	afterAssignments, err := dataStore.GetWorkspaceMCPServers(ctx, afterDefaults.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMCPServers() after disabling defaults error = %v", err)
	}
	if len(afterAssignments) != 1 || afterAssignments[0].Enabled {
		t.Fatalf("workspace inherited disabled MCP server = %#v", afterAssignments)
	}
}

func TestMCPServerValidationRejectsUnsafeHTTPConfiguration(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(ctx, filepath.Join(t.TempDir(), "materialmind.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	_, err = dataStore.CreateMCPServer(ctx, MCPServer{
		Name:      "Unsafe",
		Transport: MCPTransportHTTP,
		URL:       "https://user:secret@example.com/mcp",
		AuthType:  MCPAuthNone,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateMCPServer() error = %v, want ErrInvalidInput", err)
	}
	_, err = dataStore.CreateMCPServer(ctx, MCPServer{
		Name:      "Duplicate authorization",
		Transport: MCPTransportHTTP,
		URL:       "https://example.com/mcp",
		Headers: []MCPVariableBinding{{
			Name:        "Authorization",
			ValueEnvVar: "MCP_SOURCE_TOKEN",
		}},
		AuthType: MCPAuthNone,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateMCPServer() authorization header error = %v, want ErrInvalidInput", err)
	}
}
