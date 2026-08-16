package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

var httpHeaderName = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

func (s *Store) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, mcpServerSelect+` ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	items := make([]MCPServer, 0)
	for rows.Next() {
		item, err := scanMCPServer(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close MCP server rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	permissions, err := loadMCPToolPermissions(
		ctx,
		s.db,
		`SELECT mcp_server_id, tool_name, confirmation_mode
		 FROM mcp_default_tool_permissions ORDER BY tool_name`,
	)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].DefaultToolPermissions = append(
			[]MCPToolPermission{},
			permissions[items[index].ID]...,
		)
	}
	return items, nil
}

func (s *Store) GetMCPServer(ctx context.Context, id string) (MCPServer, error) {
	item, err := scanMCPServer(s.db.QueryRowContext(ctx, mcpServerSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPServer{}, ErrNotFound
	}
	if err != nil {
		return MCPServer{}, err
	}
	permissions, err := loadMCPToolPermissions(
		ctx,
		s.db,
		`SELECT mcp_server_id, tool_name, confirmation_mode
		 FROM mcp_default_tool_permissions WHERE mcp_server_id = ? ORDER BY tool_name`,
		id,
	)
	if err != nil {
		return MCPServer{}, err
	}
	item.DefaultToolPermissions = append([]MCPToolPermission{}, permissions[id]...)
	return item, nil
}

func (s *Store) CreateMCPServer(ctx context.Context, input MCPServer) (MCPServer, error) {
	normalized, err := normalizeMCPServer(input)
	if err != nil {
		return MCPServer{}, err
	}
	now := time.Now().UTC()
	normalized.ID = uuid.NewString()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	arguments, environment, headers, scopes, err := encodeMCPConfig(normalized)
	if err != nil {
		return MCPServer{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_servers(
		id, name, transport, command, arguments_json, environment_json, url, headers_json,
		auth_type, bearer_token_env_var, oauth_client_mode, oauth_client_id,
		oauth_client_secret_env_var, oauth_scopes_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, normalized.Name, normalized.Transport, normalized.Command, arguments,
		environment, normalized.URL, headers, normalized.AuthType, normalized.BearerTokenEnvVar,
		normalized.OAuthClientMode, normalized.OAuthClientID,
		normalized.OAuthClientSecretEnvVar, scopes, formatTime(now), formatTime(now))
	if err != nil {
		return MCPServer{}, fmt.Errorf("create MCP server: %w", err)
	}
	return s.GetMCPServer(ctx, normalized.ID)
}

func (s *Store) UpdateMCPServer(ctx context.Context, id string, input MCPServer) (MCPServer, error) {
	if _, err := s.GetMCPServer(ctx, id); err != nil {
		return MCPServer{}, err
	}
	normalized, err := normalizeMCPServer(input)
	if err != nil {
		return MCPServer{}, err
	}
	arguments, environment, headers, scopes, err := encodeMCPConfig(normalized)
	if err != nil {
		return MCPServer{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_servers SET
		name = ?, transport = ?, command = ?, arguments_json = ?, environment_json = ?,
		url = ?, headers_json = ?, auth_type = ?, bearer_token_env_var = ?,
		oauth_client_mode = ?, oauth_client_id = ?, oauth_client_secret_env_var = ?,
		oauth_scopes_json = ?, updated_at = ?
		WHERE id = ?`,
		normalized.Name, normalized.Transport, normalized.Command, arguments, environment,
		normalized.URL, headers, normalized.AuthType, normalized.BearerTokenEnvVar,
		normalized.OAuthClientMode, normalized.OAuthClientID,
		normalized.OAuthClientSecretEnvVar, scopes, formatTime(time.Now()), id)
	if err != nil {
		return MCPServer{}, fmt.Errorf("update MCP server: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return MCPServer{}, ErrNotFound
	}
	return s.GetMCPServer(ctx, id)
}

func (s *Store) UpdateMCPServerDefaults(
	ctx context.Context,
	id string,
	enabled bool,
	confirmationMode string,
	toolPermissions []MCPToolPermission,
) (MCPServer, error) {
	if err := validateMCPConfirmationMode(confirmationMode); err != nil {
		return MCPServer{}, err
	}
	normalized, err := normalizeMCPToolPermissions(toolPermissions)
	if err != nil {
		return MCPServer{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MCPServer{}, fmt.Errorf("begin MCP default update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE mcp_servers
		SET default_enabled = ?, default_confirmation_mode = ?, updated_at = ?
		WHERE id = ?`, enabled, confirmationMode, formatTime(time.Now()), id)
	if err != nil {
		return MCPServer{}, fmt.Errorf("update MCP defaults: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return MCPServer{}, ErrNotFound
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM mcp_default_tool_permissions WHERE mcp_server_id = ?`,
		id,
	); err != nil {
		return MCPServer{}, fmt.Errorf("clear MCP default tool permissions: %w", err)
	}
	for _, permission := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_default_tool_permissions(
			mcp_server_id, tool_name, confirmation_mode
		) VALUES(?, ?, ?)`, id, permission.ToolName, permission.ConfirmationMode); err != nil {
			return MCPServer{}, fmt.Errorf("insert MCP default tool permission: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return MCPServer{}, fmt.Errorf("commit MCP default update: %w", err)
	}
	return s.GetMCPServer(ctx, id)
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return fmt.Errorf("MCP server is used by a workspace or session: %w", ErrConflict)
		}
		return fmt.Errorf("delete MCP server: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetWorkspaceMCPServers(ctx context.Context, workspaceID string) ([]MCPServerAssignment, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return nil, err
	}
	servers, err := s.ListMCPServers(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT mcp_server_id, confirmation_mode
		FROM workspace_mcp_servers WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace MCP servers: %w", err)
	}
	modes := make(map[string]string)
	for rows.Next() {
		var serverID, mode string
		if err := rows.Scan(&serverID, &mode); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan workspace MCP server: %w", err)
		}
		modes[serverID] = mode
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close workspace MCP server rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list workspace MCP servers: %w", err)
	}
	toolPermissions, err := loadMCPToolPermissions(
		ctx,
		s.db,
		`SELECT mcp_server_id, tool_name, confirmation_mode
		 FROM workspace_mcp_tool_permissions WHERE workspace_id = ?`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	result := make([]MCPServerAssignment, 0, len(servers))
	for _, server := range servers {
		mode, enabled := modes[server.ID]
		if !enabled {
			mode = MCPConfirmationAsk
		}
		result = append(result, MCPServerAssignment{
			Server:           server,
			Enabled:          enabled,
			ConfirmationMode: mode,
			ToolPermissions:  toolPermissions[server.ID],
		})
	}
	return result, nil
}

func (s *Store) ReplaceWorkspaceMCPServers(
	ctx context.Context,
	workspaceID string,
	assignments []MCPServerAssignment,
) ([]MCPServerAssignment, error) {
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return nil, err
	}
	normalized, err := s.normalizeMCPAssignments(ctx, assignments)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin workspace MCP server update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_mcp_servers WHERE workspace_id = ?`, workspaceID); err != nil {
		return nil, fmt.Errorf("clear workspace MCP servers: %w", err)
	}
	for _, assignment := range normalized {
		if !assignment.Enabled {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_mcp_servers(
			workspace_id, mcp_server_id, confirmation_mode
		) VALUES(?, ?, ?)`, workspaceID, assignment.Server.ID, assignment.ConfirmationMode); err != nil {
			return nil, fmt.Errorf("insert workspace MCP server: %w", err)
		}
		if err := insertWorkspaceMCPToolPermissions(
			ctx,
			tx,
			workspaceID,
			assignment.Server.ID,
			assignment.ToolPermissions,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit workspace MCP server update: %w", err)
	}
	return s.GetWorkspaceMCPServers(ctx, workspaceID)
}

func (s *Store) GetSessionMCPServers(ctx context.Context, sessionID string) ([]SessionMCPServer, error) {
	sessionRecord, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	query := `SELECT
		mcp_server_id, name, transport, command, arguments_json, environment_json, url,
		headers_json, auth_type, bearer_token_env_var, oauth_client_mode, oauth_client_id,
		oauth_client_secret_env_var, oauth_scopes_json, confirmation_mode
		FROM session_mcp_servers WHERE session_id = ? ORDER BY lower(name), mcp_server_id`
	if sessionRecord.RuntimeType == RuntimeADK {
		query = `SELECT
			configured.mcp_server_id, current.name, current.transport, current.command,
			current.arguments_json, current.environment_json, current.url,
			current.headers_json, current.auth_type, current.bearer_token_env_var,
			current.oauth_client_mode, current.oauth_client_id,
			current.oauth_client_secret_env_var, current.oauth_scopes_json,
			configured.confirmation_mode
			FROM session_mcp_servers AS configured
			JOIN mcp_servers AS current ON current.id = configured.mcp_server_id
			WHERE configured.session_id = ?
			ORDER BY lower(current.name), configured.mcp_server_id`
	}
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session MCP servers: %w", err)
	}
	defer rows.Close()
	items := make([]SessionMCPServer, 0)
	for rows.Next() {
		var item SessionMCPServer
		var arguments, environment, headers, scopes string
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Transport, &item.Command, &arguments, &environment,
			&item.URL, &headers, &item.AuthType, &item.BearerTokenEnvVar,
			&item.OAuthClientMode, &item.OAuthClientID, &item.OAuthClientSecretEnvVar,
			&scopes, &item.ConfirmationMode,
		); err != nil {
			return nil, fmt.Errorf("scan session MCP server: %w", err)
		}
		if err := decodeMCPConfig(&item.MCPServer, arguments, environment, headers, scopes); err != nil {
			return nil, err
		}
		setMCPAvailability(&item.MCPServer)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list session MCP servers: %w", err)
	}
	permissions, err := loadMCPToolPermissions(
		ctx,
		s.db,
		`SELECT mcp_server_id, tool_name, confirmation_mode
		 FROM session_mcp_tool_permissions WHERE session_id = ?`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].ToolPermissions = permissions[items[index].ID]
	}
	return items, nil
}

func (s *Store) ReplaceSessionMCPServers(
	ctx context.Context,
	sessionID string,
	assignments []MCPServerAssignment,
) ([]SessionMCPServer, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	normalized, err := s.normalizeMCPAssignments(ctx, assignments)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session MCP server update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_mcp_servers WHERE session_id = ?`, sessionID); err != nil {
		return nil, fmt.Errorf("clear session MCP servers: %w", err)
	}
	for _, assignment := range normalized {
		if !assignment.Enabled {
			continue
		}
		if err := insertSessionMCPServer(ctx, tx, sessionID, assignment); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session MCP server update: %w", err)
	}
	return s.GetSessionMCPServers(ctx, sessionID)
}

func copyDefaultMCPServers(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_mcp_servers(
		workspace_id, mcp_server_id, confirmation_mode
	)
	SELECT ?, id, default_confirmation_mode
	FROM mcp_servers
	WHERE default_enabled = 1`, workspaceID); err != nil {
		return fmt.Errorf("copy default MCP servers to workspace: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_mcp_tool_permissions(
		workspace_id, mcp_server_id, tool_name, confirmation_mode
	)
	SELECT ?, permissions.mcp_server_id, permissions.tool_name, permissions.confirmation_mode
	FROM mcp_default_tool_permissions AS permissions
	JOIN mcp_servers AS servers ON servers.id = permissions.mcp_server_id
	WHERE servers.default_enabled = 1`, workspaceID); err != nil {
		return fmt.Errorf("copy default MCP tool permissions to workspace: %w", err)
	}
	return nil
}

func copyWorkspaceMCPServers(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID, sessionID string,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT
		s.id, s.name, s.transport, s.command, s.arguments_json, s.environment_json, s.url,
		s.headers_json, s.auth_type, s.bearer_token_env_var, s.oauth_client_mode,
		s.oauth_client_id, s.oauth_client_secret_env_var, s.oauth_scopes_json,
		w.confirmation_mode
		FROM workspace_mcp_servers AS w
		JOIN mcp_servers AS s ON s.id = w.mcp_server_id
		WHERE w.workspace_id = ?`, workspaceID)
	if err != nil {
		return fmt.Errorf("load inherited MCP servers: %w", err)
	}
	type inheritedServer struct {
		assignment  MCPServerAssignment
		arguments   string
		environment string
		headers     string
		scopes      string
	}
	inherited := make([]inheritedServer, 0)
	for rows.Next() {
		var item inheritedServer
		item.assignment.Enabled = true
		if err := rows.Scan(
			&item.assignment.Server.ID, &item.assignment.Server.Name,
			&item.assignment.Server.Transport, &item.assignment.Server.Command,
			&item.arguments, &item.environment, &item.assignment.Server.URL, &item.headers,
			&item.assignment.Server.AuthType, &item.assignment.Server.BearerTokenEnvVar,
			&item.assignment.Server.OAuthClientMode, &item.assignment.Server.OAuthClientID,
			&item.assignment.Server.OAuthClientSecretEnvVar, &item.scopes,
			&item.assignment.ConfirmationMode,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan inherited MCP server: %w", err)
		}
		if err := decodeMCPConfig(
			&item.assignment.Server,
			item.arguments,
			item.environment,
			item.headers,
			item.scopes,
		); err != nil {
			rows.Close()
			return err
		}
		inherited = append(inherited, item)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close inherited MCP server rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load inherited MCP servers: %w", err)
	}
	permissions, err := loadMCPToolPermissions(
		ctx,
		tx,
		`SELECT mcp_server_id, tool_name, confirmation_mode
		 FROM workspace_mcp_tool_permissions WHERE workspace_id = ?`,
		workspaceID,
	)
	if err != nil {
		return err
	}
	for _, item := range inherited {
		item.assignment.ToolPermissions = permissions[item.assignment.Server.ID]
		if err := insertSessionMCPServer(ctx, tx, sessionID, item.assignment); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetMCPOAuthMetadata(ctx context.Context, serverID string) (MCPOAuthMetadata, error) {
	var item MCPOAuthMetadata
	var scopes, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT mcp_server_id, resource, authorization_endpoint,
		token_endpoint, registration_endpoint, scopes_json, client_id, token_auth_method,
		updated_at FROM mcp_oauth_metadata WHERE mcp_server_id = ?`, serverID).Scan(
		&item.MCPServerID, &item.Resource, &item.AuthorizationEndpoint, &item.TokenEndpoint,
		&item.RegistrationEndpoint, &scopes, &item.ClientID, &item.TokenAuthMethod, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPOAuthMetadata{}, ErrNotFound
	}
	if err != nil {
		return MCPOAuthMetadata{}, fmt.Errorf("get MCP OAuth metadata: %w", err)
	}
	if err := json.Unmarshal([]byte(scopes), &item.Scopes); err != nil {
		return MCPOAuthMetadata{}, fmt.Errorf("decode MCP OAuth scopes: %w", err)
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return MCPOAuthMetadata{}, err
	}
	return item, nil
}

func (s *Store) UpsertMCPOAuthMetadata(ctx context.Context, item MCPOAuthMetadata) error {
	if _, err := s.GetMCPServer(ctx, item.MCPServerID); err != nil {
		return err
	}
	scopes, err := json.Marshal(item.Scopes)
	if err != nil {
		return fmt.Errorf("encode MCP OAuth scopes: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_oauth_metadata(
		mcp_server_id, resource, authorization_endpoint, token_endpoint,
		registration_endpoint, scopes_json, client_id, token_auth_method, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(mcp_server_id) DO UPDATE SET
		resource = excluded.resource,
		authorization_endpoint = excluded.authorization_endpoint,
		token_endpoint = excluded.token_endpoint,
		registration_endpoint = excluded.registration_endpoint,
		scopes_json = excluded.scopes_json,
		client_id = excluded.client_id,
		token_auth_method = excluded.token_auth_method,
		updated_at = excluded.updated_at`,
		item.MCPServerID, item.Resource, item.AuthorizationEndpoint, item.TokenEndpoint,
		item.RegistrationEndpoint, string(scopes), item.ClientID, item.TokenAuthMethod,
		formatTime(now))
	if err != nil {
		return fmt.Errorf("save MCP OAuth metadata: %w", err)
	}
	return nil
}

func (s *Store) DeleteMCPOAuthMetadata(ctx context.Context, serverID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_metadata WHERE mcp_server_id = ?`, serverID); err != nil {
		return fmt.Errorf("delete MCP OAuth metadata: %w", err)
	}
	return nil
}

const mcpServerSelect = `SELECT
	id, name, transport, command, arguments_json, environment_json, url, headers_json,
	auth_type, bearer_token_env_var, oauth_client_mode, oauth_client_id,
	oauth_client_secret_env_var, oauth_scopes_json, default_enabled,
	default_confirmation_mode, created_at, updated_at
	FROM mcp_servers`

func scanMCPServer(scanner interface{ Scan(...any) error }) (MCPServer, error) {
	var item MCPServer
	var arguments, environment, headers, scopes, createdAt, updatedAt string
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.Transport, &item.Command, &arguments, &environment,
		&item.URL, &headers, &item.AuthType, &item.BearerTokenEnvVar,
		&item.OAuthClientMode, &item.OAuthClientID, &item.OAuthClientSecretEnvVar,
		&scopes, &item.DefaultEnabled, &item.DefaultConfirmationMode, &createdAt, &updatedAt,
	); err != nil {
		return MCPServer{}, err
	}
	if err := decodeMCPConfig(&item, arguments, environment, headers, scopes); err != nil {
		return MCPServer{}, err
	}
	var err error
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return MCPServer{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return MCPServer{}, err
	}
	setMCPAvailability(&item)
	return item, nil
}

func normalizeMCPServer(input MCPServer) (MCPServer, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return MCPServer{}, fmt.Errorf("%w: MCP server name is required", ErrInvalidInput)
	}
	input.Transport = strings.ToLower(strings.TrimSpace(input.Transport))
	input.Command = strings.TrimSpace(input.Command)
	input.URL = strings.TrimSpace(input.URL)
	input.AuthType = strings.ToLower(strings.TrimSpace(input.AuthType))
	input.BearerTokenEnvVar = strings.TrimSpace(input.BearerTokenEnvVar)
	input.OAuthClientMode = strings.ToLower(strings.TrimSpace(input.OAuthClientMode))
	input.OAuthClientID = strings.TrimSpace(input.OAuthClientID)
	input.OAuthClientSecretEnvVar = strings.TrimSpace(input.OAuthClientSecretEnvVar)
	var err error
	input.Arguments, err = normalizeStringList(input.Arguments, false)
	if err != nil {
		return MCPServer{}, fmt.Errorf("%w: arguments: %v", ErrInvalidInput, err)
	}
	input.Environment, err = normalizeMCPBindings(input.Environment, false)
	if err != nil {
		return MCPServer{}, err
	}
	input.Headers, err = normalizeMCPBindings(input.Headers, true)
	if err != nil {
		return MCPServer{}, err
	}
	input.OAuthScopes, err = normalizeStringList(input.OAuthScopes, true)
	if err != nil {
		return MCPServer{}, fmt.Errorf("%w: OAuth scopes: %v", ErrInvalidInput, err)
	}
	switch input.Transport {
	case MCPTransportStdio:
		if input.Command == "" {
			return MCPServer{}, fmt.Errorf("%w: command is required for a stdio MCP server", ErrInvalidInput)
		}
		input.URL = ""
		input.Headers = []MCPVariableBinding{}
		input.AuthType = MCPAuthNone
		input.BearerTokenEnvVar = ""
		input.OAuthClientMode = MCPOAuthClientDynamic
		input.OAuthClientID = ""
		input.OAuthClientSecretEnvVar = ""
		input.OAuthScopes = []string{}
	case MCPTransportHTTP:
		parsed, err := url.Parse(input.URL)
		if err != nil || parsed.Host == "" || !slices.Contains([]string{"http", "https"}, parsed.Scheme) {
			return MCPServer{}, fmt.Errorf("%w: HTTP MCP server URL must be an absolute HTTP(S) URL", ErrInvalidInput)
		}
		if parsed.User != nil || parsed.Fragment != "" {
			return MCPServer{}, fmt.Errorf("%w: HTTP MCP server URL cannot contain user information or a fragment", ErrInvalidInput)
		}
		input.Command = ""
		input.Arguments = []string{}
		input.Environment = []MCPVariableBinding{}
		switch input.AuthType {
		case "", MCPAuthNone:
			input.AuthType = MCPAuthNone
			input.BearerTokenEnvVar = ""
			input.OAuthClientMode = MCPOAuthClientDynamic
			input.OAuthClientID = ""
			input.OAuthClientSecretEnvVar = ""
			input.OAuthScopes = []string{}
		case MCPAuthBearerEnv:
			if !environmentName.MatchString(input.BearerTokenEnvVar) {
				return MCPServer{}, fmt.Errorf("%w: bearer token environment variable is invalid", ErrInvalidInput)
			}
			input.OAuthClientMode = MCPOAuthClientDynamic
			input.OAuthClientID = ""
			input.OAuthClientSecretEnvVar = ""
			input.OAuthScopes = []string{}
		case MCPAuthOAuth:
			input.BearerTokenEnvVar = ""
			switch input.OAuthClientMode {
			case "", MCPOAuthClientDynamic:
				input.OAuthClientMode = MCPOAuthClientDynamic
				input.OAuthClientID = ""
				input.OAuthClientSecretEnvVar = ""
			case MCPOAuthClientPreRegistered:
				if input.OAuthClientID == "" {
					return MCPServer{}, fmt.Errorf("%w: OAuth client ID is required for a pre-registered client", ErrInvalidInput)
				}
				if input.OAuthClientSecretEnvVar != "" &&
					!environmentName.MatchString(input.OAuthClientSecretEnvVar) {
					return MCPServer{}, fmt.Errorf("%w: OAuth client secret environment variable is invalid", ErrInvalidInput)
				}
			default:
				return MCPServer{}, fmt.Errorf("%w: unsupported OAuth client mode %q", ErrInvalidInput, input.OAuthClientMode)
			}
		default:
			return MCPServer{}, fmt.Errorf("%w: unsupported MCP authentication type %q", ErrInvalidInput, input.AuthType)
		}
	default:
		return MCPServer{}, fmt.Errorf("%w: unsupported MCP transport %q", ErrInvalidInput, input.Transport)
	}
	setMCPAvailability(&input)
	return input, nil
}

func normalizeMCPBindings(
	bindings []MCPVariableBinding,
	headers bool,
) ([]MCPVariableBinding, error) {
	result := make([]MCPVariableBinding, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		binding.Name = strings.TrimSpace(binding.Name)
		binding.ValueEnvVar = strings.TrimSpace(binding.ValueEnvVar)
		if binding.Name == "" || !environmentName.MatchString(binding.ValueEnvVar) {
			return nil, fmt.Errorf("%w: MCP variable bindings require a name and valid environment variable", ErrInvalidInput)
		}
		key := binding.Name
		if headers {
			if !httpHeaderName.MatchString(binding.Name) {
				return nil, fmt.Errorf("%w: invalid HTTP header name %q", ErrInvalidInput, binding.Name)
			}
			binding.Name = http.CanonicalHeaderKey(binding.Name)
			key = strings.ToLower(binding.Name)
			if key == "authorization" {
				return nil, fmt.Errorf("%w: use the authentication setting instead of an Authorization header binding", ErrInvalidInput)
			}
		} else if !environmentName.MatchString(binding.Name) {
			return nil, fmt.Errorf("%w: invalid child environment variable %q", ErrInvalidInput, binding.Name)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate MCP variable binding %q", ErrInvalidInput, binding.Name)
		}
		seen[key] = struct{}{}
		result = append(result, binding)
	}
	return result, nil
}

func normalizeStringList(values []string, deduplicate bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("values cannot contain a null byte")
		}
		if deduplicate {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
		}
		result = append(result, value)
	}
	return result, nil
}

func normalizeMCPToolPermissions(
	permissions []MCPToolPermission,
) ([]MCPToolPermission, error) {
	result := make([]MCPToolPermission, 0, len(permissions))
	seen := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		permission.ToolName = strings.TrimSpace(permission.ToolName)
		if permission.ToolName == "" {
			return nil, fmt.Errorf("%w: MCP tool name is required", ErrInvalidInput)
		}
		if err := validateMCPConfirmationMode(permission.ConfirmationMode); err != nil {
			return nil, err
		}
		if _, duplicate := seen[permission.ToolName]; duplicate {
			return nil, fmt.Errorf("%w: duplicate MCP tool permission %q", ErrInvalidInput, permission.ToolName)
		}
		seen[permission.ToolName] = struct{}{}
		result = append(result, permission)
	}
	slices.SortFunc(result, func(left, right MCPToolPermission) int {
		return strings.Compare(left.ToolName, right.ToolName)
	})
	return result, nil
}

func (s *Store) normalizeMCPAssignments(
	ctx context.Context,
	assignments []MCPServerAssignment,
) ([]MCPServerAssignment, error) {
	result := make([]MCPServerAssignment, 0, len(assignments))
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		serverID := strings.TrimSpace(assignment.Server.ID)
		if serverID == "" {
			return nil, fmt.Errorf("%w: MCP server ID is required", ErrInvalidInput)
		}
		if _, duplicate := seen[serverID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate MCP server assignment %q", ErrInvalidInput, serverID)
		}
		seen[serverID] = struct{}{}
		server, err := s.GetMCPServer(ctx, serverID)
		if err != nil {
			return nil, err
		}
		assignment.Server = server
		if !assignment.Enabled {
			assignment.ConfirmationMode = MCPConfirmationAsk
			assignment.ToolPermissions = []MCPToolPermission{}
			result = append(result, assignment)
			continue
		}
		if err := validateMCPConfirmationMode(assignment.ConfirmationMode); err != nil {
			return nil, err
		}
		assignment.ToolPermissions, err = normalizeMCPToolPermissions(assignment.ToolPermissions)
		if err != nil {
			return nil, err
		}
		result = append(result, assignment)
	}
	return result, nil
}

func validateMCPConfirmationMode(mode string) error {
	if mode != MCPConfirmationAllow && mode != MCPConfirmationAsk {
		return fmt.Errorf("%w: unsupported confirmation mode %q", ErrInvalidInput, mode)
	}
	return nil
}

func encodeMCPConfig(server MCPServer) (string, string, string, string, error) {
	arguments, err := json.Marshal(server.Arguments)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode MCP arguments: %w", err)
	}
	environment, err := json.Marshal(server.Environment)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode MCP environment: %w", err)
	}
	headers, err := json.Marshal(server.Headers)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode MCP headers: %w", err)
	}
	scopes, err := json.Marshal(server.OAuthScopes)
	if err != nil {
		return "", "", "", "", fmt.Errorf("encode MCP OAuth scopes: %w", err)
	}
	return string(arguments), string(environment), string(headers), string(scopes), nil
}

func decodeMCPConfig(
	server *MCPServer,
	arguments, environment, headers, scopes string,
) error {
	if err := json.Unmarshal([]byte(arguments), &server.Arguments); err != nil {
		return fmt.Errorf("decode MCP arguments: %w", err)
	}
	if err := json.Unmarshal([]byte(environment), &server.Environment); err != nil {
		return fmt.Errorf("decode MCP environment: %w", err)
	}
	if err := json.Unmarshal([]byte(headers), &server.Headers); err != nil {
		return fmt.Errorf("decode MCP headers: %w", err)
	}
	if err := json.Unmarshal([]byte(scopes), &server.OAuthScopes); err != nil {
		return fmt.Errorf("decode MCP OAuth scopes: %w", err)
	}
	return nil
}

func setMCPAvailability(server *MCPServer) {
	server.Available = true
	server.CredentialAvailable = true
	for _, binding := range append(slices.Clone(server.Environment), server.Headers...) {
		if strings.TrimSpace(os.Getenv(binding.ValueEnvVar)) == "" {
			server.CredentialAvailable = false
		}
	}
	switch server.Transport {
	case MCPTransportStdio:
		if _, err := exec.LookPath(server.Command); err != nil {
			server.Available = false
		}
	case MCPTransportHTTP:
		if server.AuthType == MCPAuthBearerEnv &&
			strings.TrimSpace(os.Getenv(server.BearerTokenEnvVar)) == "" {
			server.CredentialAvailable = false
		}
		if server.AuthType == MCPAuthOAuth &&
			server.OAuthClientMode == MCPOAuthClientPreRegistered &&
			server.OAuthClientSecretEnvVar != "" &&
			strings.TrimSpace(os.Getenv(server.OAuthClientSecretEnvVar)) == "" {
			server.CredentialAvailable = false
		}
	}
	server.Available = server.Available && server.CredentialAvailable
}

func loadMCPToolPermissions(
	ctx context.Context,
	queryer permissionQueryer,
	query string,
	args ...any,
) (map[string][]MCPToolPermission, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list MCP tool permissions: %w", err)
	}
	defer rows.Close()
	result := make(map[string][]MCPToolPermission)
	for rows.Next() {
		var serverID string
		var permission MCPToolPermission
		if err := rows.Scan(&serverID, &permission.ToolName, &permission.ConfirmationMode); err != nil {
			return nil, fmt.Errorf("scan MCP tool permission: %w", err)
		}
		result[serverID] = append(result[serverID], permission)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list MCP tool permissions: %w", err)
	}
	return result, nil
}

func insertWorkspaceMCPToolPermissions(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID, serverID string,
	permissions []MCPToolPermission,
) error {
	for _, permission := range permissions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_mcp_tool_permissions(
			workspace_id, mcp_server_id, tool_name, confirmation_mode
		) VALUES(?, ?, ?, ?)`, workspaceID, serverID, permission.ToolName, permission.ConfirmationMode); err != nil {
			return fmt.Errorf("insert workspace MCP tool permission: %w", err)
		}
	}
	return nil
}

func insertSessionMCPServer(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	assignment MCPServerAssignment,
) error {
	arguments, environment, headers, scopes, err := encodeMCPConfig(assignment.Server)
	if err != nil {
		return err
	}
	server := assignment.Server
	if _, err := tx.ExecContext(
		ctx, `INSERT INTO session_mcp_servers(
		session_id, mcp_server_id, name, transport, command, arguments_json,
		environment_json, url, headers_json, auth_type, bearer_token_env_var,
		oauth_client_mode, oauth_client_id, oauth_client_secret_env_var,
		oauth_scopes_json, confirmation_mode
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, server.ID, server.Name, server.Transport, server.Command, arguments,
		environment, server.URL, headers, server.AuthType, server.BearerTokenEnvVar,
		server.OAuthClientMode, server.OAuthClientID, server.OAuthClientSecretEnvVar,
		scopes, assignment.ConfirmationMode,
	); err != nil {
		return fmt.Errorf("insert session MCP server: %w", err)
	}
	for _, permission := range assignment.ToolPermissions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_mcp_tool_permissions(
			session_id, mcp_server_id, tool_name, confirmation_mode
		) VALUES(?, ?, ?, ?)`, sessionID, server.ID, permission.ToolName, permission.ConfirmationMode); err != nil {
			return fmt.Errorf("insert session MCP tool permission: %w", err)
		}
	}
	return nil
}
