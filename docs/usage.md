# Using MaterialMind

MaterialMind is a local AI workspace. It connects configured models or local ACP agents to a selected project, then shows the conversation, tool calls, approvals, and saved session history in the browser.

## Start the app

```bash
npm ci --prefix web
npm run build
TEAM_OPENAI_TOKEN=your-token ./dist/materialmind
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080).

## Set up a model

MaterialMind can store a provider credential in the operating-system keyring or read it from an environment variable named in Settings. Credential values are write-only in the UI: they are not returned to the frontend or stored in SQLite. Gemini uses the credential as an API key; Anthropic- and OpenAI-compatible providers use it as a Bearer token.

For environment-based authentication, export the configured variable before starting the backend:

```bash
export TEAM_CLAUDE_TOKEN=your-token
export TEAM_OPENAI_TOKEN=another-token
export TEAM_GEMINI_API_KEY=your-api-key
```

In Settings, add a provider, choose the API compatibility, then add one or more models. The supported provider types are:

- Anthropic compatible
- Google Gemini API
- OpenAI compatible, Chat Completions
- OpenAI compatible, Responses

Leave the provider base URL blank to use the SDK default endpoint. For a gateway, enter the API prefix that should appear before the protocol endpoint. Gemini base URLs identify the API root; the Google SDK appends its API version and model resource paths.

The default `MATERIALMIND_CREDENTIAL_STORE=auto` mode uses the operating-system keyring when available and otherwise keeps credentials in backend process memory. Use `keyring` to require persistent OS storage or `memory` for an explicit non-persistent setup. Existing providers configured with an environment-variable name continue using that environment variable after upgrading.

## Create a workspace and session

Register a project root directory as a workspace. New sessions copy the workspace tool permissions when they are created, so later workspace changes do not rewrite existing sessions.

Each session has one runtime:

- MaterialMind ADK, which uses configured providers, models, tools, and permissions.
- ACP agent, which starts a configured local command and communicates over Agent Client Protocol.

The runtime is selected when the session is created.

ACP agents can request structured form input or direct you to an external URL during a turn. MaterialMind keeps the request in the originating activity panel, returns accepted form values to the agent, and marks URL flows complete when the agent reports completion.

Use **Inspect ACP capabilities** on an agent runtime to see its negotiated protocol features. Agent-managed authentication methods can be started there. When the agent advertises session discovery and load or resume support, existing agent sessions can be imported into a MaterialMind workspace whose root matches the reported working directory.

ACP `fs/read_text_file` and `fs/write_text_file` requests use the session's `read_file` and `edit_file` policies. A configured confirmation is shown in the same activity timeline, and a full-file write includes a diff. These protocol callbacks do not sandbox the ACP process itself; only configure ACP commands you trust.

## Connect MCP servers

Open Settings and add an MCP server:

- **Local command (stdio):** enter a command, one argument per line, and optional environment mappings in `CHILD_NAME=BACKEND_ENV_NAME` form.
- **Streamable HTTP:** enter the MCP endpoint and optional header mappings in `Header-Name=BACKEND_ENV_NAME` form.

HTTP servers support no authentication, a bearer token read from an environment variable, or OAuth 2.1. For OAuth, choose dynamic client registration or enter a pre-registered client ID and an optional client-secret environment-variable name. Select **Connect** to complete authorization in the browser, then use **Test and list capabilities** to verify discovery.

Successful tool discovery also shows the protocol version negotiated with the server. Servers can request structured form input or ask you to open an external authorization URL while a tool is running. These requests appear in the originating activity panel and must be accepted, declined, or cancelled before that tool continues.

If an enabled server advertises resources or prompts, select the composer plus button and choose **MCP context**. A resource is attached to the pending message; a prompt is expanded with the entered arguments and added as text or attachments. Content is fetched only from servers assigned to that session.

Tools that advertise an MCP App `ui://` resource can render an inline result. MaterialMind loads the HTML through the MCP server, adds a restrictive content-security policy, and runs it in an iframe that permits scripts but not same-origin access, forms, popups, or downloads. The normal tool result remains available as a fallback. App-initiated server tool calls are not exposed in this first host implementation.

MaterialMind stores configured MCP bearer tokens and client secrets only in the backend environment. OAuth refresh tokens and dynamically issued client secrets use the configured credential store; access tokens stay in memory. In `auto` mode, an unavailable keyring falls back to memory for the current backend process.

After adding a server, open a workspace's Permissions page. Enable the server, choose whether its tools can run without asking or require confirmation, and optionally override individual tools. MCP servers are disabled by default. New sessions copy the workspace's enabled servers and confirmation rules; changing those workspace assignments later does not rewrite existing sessions. ADK sessions resolve the current command, arguments, environment, URL, and authentication configuration from the shared MCP server definition, so edits to that definition apply when the server reconnects.

For MaterialMind ADK sessions, an MCP call that requires confirmation appears in the activity timeline with the server, tool, and arguments. Allowing it returns the MCP result to the model; refusing it returns the refusal and optional reason.

ACP sessions receive enabled MCP server definitions, but the ACP agent manages their approval and sandboxing behavior. An established ACP session keeps its server configuration fixed. OAuth-protected HTTP MCP servers used through ACP require the OS keyring so MaterialMind's bridge process can access the refresh token. When supported by the ACP agent, repository-level filesystem permission is also passed as an additional session directory.

MaterialMind also adds a private, session-scoped MCP server to ACP sessions for `read_session_notes` and `update_session_notes`. These calls return to the backend through a random session token, use the same session permissions and approval UI as the MaterialMind runtime, and appear once in the activity timeline. Existing ACP sessions receive the internal server when they are restored after a backend restart.

## Work with tools

MaterialMind can inspect directories, read files by line range, search with `rg`, edit files, fetch public HTTP(S) text, and run non-interactive commands.

Tools can be configured to run without interruption or ask before every call. Filesystem tools also have a hard boundary: workspace, nearest Git or Jujutsu repository root, or all files visible to the backend process.

`run_command` asks before every call by default. Approved commands run with the backend user's permissions and inherit the backend environment. In the MaterialMind runtime, each command starts as soon as it is approved and can run concurrently with commands already in progress. ACP agents control when their approved calls start, so an approved ACP call remains queued until the agent reports execution progress or output. Pending approvals are scoped to the active run and are not saved as replayable command results.

MaterialMind runtime and ACP agents can explicitly read and replace concise Markdown session notes. Notes are limited to 16 KiB and use revisions to prevent parallel updates from overwriting each other. Their contents are not loaded into prompts automatically and context compaction never reads or changes them. Each read or update remains visible in the activity timeline; users can change either tool from automatic execution to confirmation in workspace or session permissions.

## Use skills and sub-agents

MaterialMind can discover agent skills in the workspace, parent directories, and the user-level `.agents/skills` directory. Skills are loaded only when the model decides they match the request.

Built-in sub-agents can perform bounded read-only discovery and review work while preserving the session permissions and transcript.

## Manage saved data

Open **Settings > Data** to choose how long idle sessions are kept. Keeping sessions indefinitely is the default. Selecting a retention period removes idle sessions whose last update is older than that period, including their runs and attachments. Active sessions are not removed.

The same page can download a consistent SQLite backup while MaterialMind is running. The backup contains app settings, provider and runtime configuration, workspaces, conversations, runs, and attachments. Secrets held in the operating-system keyring or process memory are not part of the file.

To restore a backup, stop MaterialMind, replace `materialmind.db` in the configured data directory with the downloaded file, and restart the app. Keep a copy of the current database until the restored app has been checked. Credentials may need to be entered again when the backup is restored on another computer.

Long conversations initially show the newest part. Use **Load older** above the conversation to bring earlier messages into view without leaving the session.

## Keep it local

MaterialMind does not provide authentication. Run it on `127.0.0.1`, and treat anyone who can access it as someone who can interact with your configured models, files, commands, environment, local ACP agents, and MCP servers.

Only configure MCP servers you trust. Local stdio servers execute as the backend operating-system user and inherit its environment. Remote MCP servers receive tool arguments and can return content that becomes part of the model context.
