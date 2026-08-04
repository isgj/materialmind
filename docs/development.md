# MaterialMind Development

## Requirements

- Go 1.26.5
- Node.js 24
- npm 11
- `gofumpt` for Go formatting checks
- `ripgrep` to expose the `grep` tool

## Run locally

Install frontend dependencies once:

```bash
npm ci --prefix web
```

Run the backend and frontend in separate terminals:

```bash
npm run backend:dev
npm run frontend:dev
```

The backend listens on `127.0.0.1:8080` by default. The Angular development server runs at `http://127.0.0.1:4200` and proxies `/api` to the backend.

Other settings:

| Variable                        | Default               | Purpose                                      |
| ------------------------------- | --------------------- | -------------------------------------------- |
| `MATERIALMIND_ADDR`             | `127.0.0.1:8080`      | HTTP listen address                          |
| `MATERIALMIND_PUBLIC_URL`       | derived from address  | Browser-reachable origin for OAuth callbacks |
| `MATERIALMIND_DATA_DIR`         | user config directory | SQLite data directory                        |
| `MATERIALMIND_CREDENTIAL_STORE` | `auto`                | `auto`, `keyring`, or `memory` for OAuth     |

Restart the backend after changing provider credential environment variables.

## Provider details

Each provider has a name, API compatibility, optional base URL, and optional credential environment-variable name. A provider can own multiple models. Gemini providers use the configured credential as an API key; Anthropic- and OpenAI-compatible providers use it as a Bearer token.

Each model has a display name, API model ID, required positive context window and output token limits, and an optional reasoning-effort default where the selected API supports it.

When adding a model, MaterialMind can load model ID suggestions from the provider's standard model-list endpoint. Anthropic-compatible providers use `GET /v1/models`, Gemini providers use the Gemini Models API, and OpenAI-compatible providers use `GET /models`.

## ACP agents

Add ACP agents in Settings. Enter the executable or command name and put each argument on a separate line. The command must be available on the backend's `PATH`; absolute executable paths are also supported.

ACP agents are trusted local processes. They inherit the backend environment and run with the backend operating-system user's filesystem and network access. MaterialMind shows permission requests sent by the ACP agent. ACP client filesystem callbacks reuse the session's `read_file` and `edit_file` boundary and confirmation policies, including a reviewable diff for writes, but those callbacks do not sandbox direct filesystem access by the ACP process.

MaterialMind advertises ACP form and URL elicitation support. Session-scoped requests use the same pending-response queue and activity-timeline form as MCP elicitation. Requests that cannot be correlated safely with an active session are cancelled rather than shown in another session.

Every ACP session also receives a private stdio MCP server that exposes the backend-owned session notes tools. The descriptor carries a random process-lifetime bearer token scoped to the local session. The child server proxies calls to the loopback backend; it never opens SQLite directly. The backend applies session permissions, waits for approvals, writes the canonical transcript items, and suppresses the ACP agent's duplicate MCP activity notifications.

The runtime capability inspector starts the configured process and reports its initialized implementation, authentication methods, and session lifecycle support. Only agent-managed authentication is actionable in the web client; terminal and environment-variable methods are reported as unsupported. Session import first verifies that the remote session is returned by `session/list` for the selected workspace root, then uses `session/load` or `session/resume` and stores the remote session ID and configuration options in the local session record.

## MCP servers

MaterialMind supports MCP over local stdio and Streamable HTTP. A stdio server is started in the workspace directory and inherits the backend environment. Explicit environment mappings can copy a backend variable into a differently named child variable. HTTP header and bearer-token configuration likewise stores only environment-variable names.

Tool discovery records the protocol version negotiated with the server and displays it in Settings. Modern tool catalogs rely on the MCP SDK's per-page TTL cache; legacy servers retain notification-driven connection caching. MaterialMind advertises form and URL elicitation support. Elicitation pauses the originating tool call for browser input without consuming that call's inactivity timeout; responses remain correlated with the originating run and tool call.

Prompt, resource, and resource-template catalogs use the same session-scoped MCP connection. Resource reads and prompt expansion reject servers that are not assigned to the session. MCP Apps are detected from tool `_meta.ui.resourceUri`; only `ui://` resources are accepted. The Angular host supplies tool input and result notifications to a script-only sandbox and does not advertise app-initiated server-tool capabilities.

The current MCP `2026-07-28` protocol deprecates the older Tasks wire vocabulary. MaterialMind therefore does not advertise the Tasks extension. Long-running tool calls continue to use progress events, cancellation, and the run stream instead of maintaining a second legacy task lifecycle.

OAuth uses authorization-server discovery, PKCE, pre-registered clients or dynamic client registration, and refresh-token rotation. OAuth metadata and public client IDs are stored in SQLite. Refresh tokens and dynamic client secrets are stored in the operating-system keyring. Access tokens remain in process memory.

Credential-store modes:

- `auto` uses the OS keyring and falls back to process memory if the keyring is unavailable.
- `keyring` requires the OS keyring and reports an error instead of falling back.
- `memory` keeps credentials only until the backend exits.

Set `MATERIALMIND_PUBLIC_URL` when the browser cannot reach the origin derived from `MATERIALMIND_ADDR`, for example when running behind a local reverse proxy.

ACP agents receive enabled MCP server definitions when their session is created or loaded. Stdio servers are passed directly. HTTP servers require the ACP agent's HTTP MCP capability; OAuth HTTP servers use MaterialMind's local stdio bridge. That OAuth bridge requires credentials in the OS keyring because it runs as a separate process. MaterialMind MCP confirmation rules govern configured external MCP servers only in ADK sessions; the ACP agent's approval and sandboxing policy governs those calls. The private session-notes MCP is the exception because its calls return through MaterialMind's own permission pipeline.

When an ACP agent advertises additional-directory support, a session whose filesystem policy permits the surrounding repository receives that repository root on session creation or restoration. MaterialMind never translates unrestricted-computer access into a filesystem root for ACP.

## Tool permissions

Workspace permissions are the template for new sessions. A session receives a complete copy when it is created, so later workspace changes do not alter existing sessions.

`fetch_url` supports exact-URL and origin rules. Exact URL rules take precedence over origin rules, redirects are evaluated independently, and private or local network destinations remain blocked.

`run_command` receives an executable and argument array, so commands are run directly without implicit shell parsing. The approval shows the resolved executable, arguments, working directory, and timeout before execution.

`grep` invokes `rg --json` directly, supports smart or explicit case matching, literal searches, and glob filters, and returns structured matching lines.

`ask_user` pauses the current ADK run until the user answers or cancels the run.

## Build

```bash
npm run build
./dist/materialmind
```

Angular writes to `internal/webui/dist/browser`, and the production Go build embeds those assets with the `embed_frontend` build tag.

Development Go builds do not require frontend artifacts. Fonts and Material Symbols are bundled locally.

SQLite uses WAL mode and foreign keys. ADK session state and events use JSON payloads inside transactional SQL tables.

The Data settings page creates online backups with SQLite `VACUUM INTO`. Session retention runs at startup, after the setting changes, and once per day while the backend remains running.

## Validate

```bash
npm run format:check
npm run vet
npm test
npm run test:embedded
npm run race
npm run build
```

## Release

`npm run build:release` creates Linux, macOS, and Windows binaries for AMD64 and ARM64 under `dist/release`, together with `checksums.txt`.
