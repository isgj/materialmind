package store

const currentSchema = `
CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	root_path TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_configurations (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	base_url TEXT NOT NULL DEFAULT '',
	bearer_token_env_var TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS llm_providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	api_compatibility TEXT NOT NULL,
	base_url TEXT NOT NULL DEFAULT '',
	bearer_token_env_var TEXT NOT NULL DEFAULT '',
	auth_type TEXT NOT NULL DEFAULT 'none'
		CHECK(auth_type IN ('none', 'bearer_env', 'bearer_keyring')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_models (
	id TEXT PRIMARY KEY,
	llm_provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE RESTRICT,
	name TEXT NOT NULL,
	model_id TEXT NOT NULL,
	context_window_tokens INTEGER NOT NULL DEFAULT 128000
		CHECK(context_window_tokens >= 1),
	max_output_tokens INTEGER NOT NULL DEFAULT 4096
		CHECK(max_output_tokens >= 1),
	reasoning_effort TEXT
		CHECK(reasoning_effort IS NULL OR reasoning_effort IN (
			'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'
		)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS acp_agents (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	command TEXT NOT NULL,
	arguments_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS app_sessions (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
	title TEXT NOT NULL,
	selected_llm_configuration_id TEXT REFERENCES llm_configurations(id) ON DELETE SET NULL,
	selected_llm_model_id TEXT REFERENCES llm_models(id) ON DELETE SET NULL,
	status TEXT NOT NULL DEFAULT 'idle',
	runtime_type TEXT NOT NULL DEFAULT 'adk'
		CHECK(runtime_type IN ('adk', 'acp')),
	acp_agent_id TEXT REFERENCES acp_agents(id) ON DELETE RESTRICT,
	acp_session_id TEXT NOT NULL DEFAULT '',
	acp_config_options_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	invocation_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	runtime_type TEXT NOT NULL DEFAULT 'adk'
		CHECK(runtime_type IN ('adk', 'acp')),
	acp_agent_id TEXT NOT NULL DEFAULT '',
	acp_agent_name TEXT NOT NULL DEFAULT '',
	llm_configuration_id TEXT NOT NULL,
	llm_configuration_name TEXT NOT NULL,
	llm_provider_id TEXT NOT NULL DEFAULT '',
	llm_provider_name TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	context_window_tokens INTEGER NOT NULL DEFAULT 128000
		CHECK(context_window_tokens >= 1),
	max_output_tokens INTEGER NOT NULL DEFAULT 4096
		CHECK(max_output_tokens >= 1),
	reasoning_effort TEXT
		CHECK(reasoning_effort IS NULL OR reasoning_effort IN (
			'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'
		)),
	base_url TEXT NOT NULL DEFAULT '',
	bearer_token_env_var TEXT NOT NULL DEFAULT '',
	user_message TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	completed_at TEXT
);

CREATE TABLE IF NOT EXISTS run_attachments (
	id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	mime_type TEXT NOT NULL,
	size INTEGER NOT NULL CHECK(size >= 0),
	content BLOB NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS adk_app_state (
	app_name TEXT PRIMARY KEY,
	state_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS adk_user_state (
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	state_json TEXT NOT NULL,
	PRIMARY KEY(app_name, user_id)
);

CREATE TABLE IF NOT EXISTS adk_sessions (
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	state_json TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(app_name, user_id, session_id)
);

CREATE TABLE IF NOT EXISTS adk_events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	event_id TEXT NOT NULL,
	invocation_id TEXT NOT NULL,
	event_time TEXT NOT NULL,
	event_json TEXT NOT NULL,
	UNIQUE(app_name, user_id, session_id, event_id),
	FOREIGN KEY(app_name, user_id, session_id)
		REFERENCES adk_sessions(app_name, user_id, session_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS workspace_tool_permissions (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	tool_name TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	filesystem_scope TEXT NOT NULL DEFAULT ''
		CHECK(filesystem_scope IN ('', 'workspace', 'repository', 'computer')),
	PRIMARY KEY(workspace_id, tool_name)
);

CREATE TABLE IF NOT EXISTS workspace_tool_permission_rules (
	workspace_id TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	matcher TEXT NOT NULL CHECK(matcher IN ('exact_url', 'origin')),
	target TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(workspace_id, tool_name, matcher, target),
	FOREIGN KEY(workspace_id, tool_name)
		REFERENCES workspace_tool_permissions(workspace_id, tool_name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_tool_permissions (
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	tool_name TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	filesystem_scope TEXT NOT NULL DEFAULT ''
		CHECK(filesystem_scope IN ('', 'workspace', 'repository', 'computer')),
	PRIMARY KEY(session_id, tool_name)
);

CREATE TABLE IF NOT EXISTS session_tool_permission_rules (
	session_id TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	matcher TEXT NOT NULL CHECK(matcher IN ('exact_url', 'origin')),
	target TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(session_id, tool_name, matcher, target),
	FOREIGN KEY(session_id, tool_name)
		REFERENCES session_tool_permissions(session_id, tool_name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS acp_transcript_items (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	id TEXT NOT NULL UNIQUE,
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	invocation_id TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL CHECK(kind IN ('message', 'thought', 'plan', 'tool_call', 'tool_result')),
	role TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	tool_name TEXT NOT NULL DEFAULT '',
	tool_call_id TEXT NOT NULL DEFAULT '',
	tool_input_json TEXT NOT NULL DEFAULT '{}',
	tool_output_json TEXT NOT NULL DEFAULT '{}',
	provider TEXT NOT NULL DEFAULT 'acp',
	model TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_servers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	transport TEXT NOT NULL CHECK(transport IN ('stdio', 'http')),
	command TEXT NOT NULL DEFAULT '',
	arguments_json TEXT NOT NULL DEFAULT '[]',
	environment_json TEXT NOT NULL DEFAULT '[]',
	url TEXT NOT NULL DEFAULT '',
	headers_json TEXT NOT NULL DEFAULT '[]',
	auth_type TEXT NOT NULL DEFAULT 'none'
		CHECK(auth_type IN ('none', 'bearer_env', 'oauth')),
	bearer_token_env_var TEXT NOT NULL DEFAULT '',
	oauth_client_mode TEXT NOT NULL DEFAULT 'dynamic'
		CHECK(oauth_client_mode IN ('dynamic', 'pre_registered')),
	oauth_client_id TEXT NOT NULL DEFAULT '',
	oauth_client_secret_env_var TEXT NOT NULL DEFAULT '',
	oauth_scopes_json TEXT NOT NULL DEFAULT '[]',
	default_enabled INTEGER NOT NULL DEFAULT 0
		CHECK(default_enabled IN (0, 1)),
	default_confirmation_mode TEXT NOT NULL DEFAULT 'ask'
		CHECK(default_confirmation_mode IN ('allow', 'ask')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_default_tool_permissions (
	mcp_server_id TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
	tool_name TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(mcp_server_id, tool_name)
);

CREATE TABLE IF NOT EXISTS workspace_mcp_servers (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	mcp_server_id TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE RESTRICT,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(workspace_id, mcp_server_id)
);

CREATE TABLE IF NOT EXISTS workspace_mcp_tool_permissions (
	workspace_id TEXT NOT NULL,
	mcp_server_id TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(workspace_id, mcp_server_id, tool_name),
	FOREIGN KEY(workspace_id, mcp_server_id)
		REFERENCES workspace_mcp_servers(workspace_id, mcp_server_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_mcp_servers (
	session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
	mcp_server_id TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE RESTRICT,
	name TEXT NOT NULL,
	transport TEXT NOT NULL CHECK(transport IN ('stdio', 'http')),
	command TEXT NOT NULL DEFAULT '',
	arguments_json TEXT NOT NULL DEFAULT '[]',
	environment_json TEXT NOT NULL DEFAULT '[]',
	url TEXT NOT NULL DEFAULT '',
	headers_json TEXT NOT NULL DEFAULT '[]',
	auth_type TEXT NOT NULL DEFAULT 'none'
		CHECK(auth_type IN ('none', 'bearer_env', 'oauth')),
	bearer_token_env_var TEXT NOT NULL DEFAULT '',
	oauth_client_mode TEXT NOT NULL DEFAULT 'dynamic'
		CHECK(oauth_client_mode IN ('dynamic', 'pre_registered')),
	oauth_client_id TEXT NOT NULL DEFAULT '',
	oauth_client_secret_env_var TEXT NOT NULL DEFAULT '',
	oauth_scopes_json TEXT NOT NULL DEFAULT '[]',
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(session_id, mcp_server_id)
);

CREATE TABLE IF NOT EXISTS session_mcp_tool_permissions (
	session_id TEXT NOT NULL,
	mcp_server_id TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	confirmation_mode TEXT NOT NULL CHECK(confirmation_mode IN ('allow', 'ask')),
	PRIMARY KEY(session_id, mcp_server_id, tool_name),
	FOREIGN KEY(session_id, mcp_server_id)
		REFERENCES session_mcp_servers(session_id, mcp_server_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mcp_oauth_metadata (
	mcp_server_id TEXT PRIMARY KEY REFERENCES mcp_servers(id) ON DELETE CASCADE,
	resource TEXT NOT NULL,
	authorization_endpoint TEXT NOT NULL,
	token_endpoint TEXT NOT NULL,
	registration_endpoint TEXT NOT NULL DEFAULT '',
	scopes_json TEXT NOT NULL DEFAULT '[]',
	client_id TEXT NOT NULL,
	token_auth_method TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS app_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_notes (
	session_id TEXT PRIMARY KEY REFERENCES app_sessions(id) ON DELETE CASCADE,
	content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) <= 16384),
	revision INTEGER NOT NULL CHECK(revision >= 1),
	updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS llm_models_provider_idx
	ON llm_models(llm_provider_id, lower(name), id);
CREATE INDEX IF NOT EXISTS acp_agents_name_idx
	ON acp_agents(lower(name), id);
CREATE INDEX IF NOT EXISTS app_sessions_workspace_idx
	ON app_sessions(workspace_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS runs_session_idx
	ON runs(session_id, created_at);
CREATE INDEX IF NOT EXISTS run_attachments_run_idx
	ON run_attachments(run_id, created_at, id);
CREATE INDEX IF NOT EXISTS adk_sessions_list_idx
	ON adk_sessions(app_name, user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS adk_events_session_idx
	ON adk_events(app_name, user_id, session_id, sequence);
CREATE INDEX IF NOT EXISTS acp_transcript_session_idx
	ON acp_transcript_items(session_id, sequence);
CREATE INDEX IF NOT EXISTS mcp_servers_name_idx
	ON mcp_servers(lower(name), id);

INSERT OR IGNORE INTO app_settings(key, value, updated_at)
VALUES('retention_days', '0', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
`
