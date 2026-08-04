# MaterialMind

MaterialMind is a local AI workspace for project-aware coding sessions.

It lets you register workspaces, configure model providers, run AI sessions, review tool use, and keep session history locally. The app is served by a Go backend with an Angular Material frontend.

## Capabilities

- Register project root directories as workspaces.
- Configure Anthropic-compatible, Gemini, and OpenAI-compatible providers, with multiple models per provider.
- Run independent sessions concurrently and switch models between turns.
- Stream agent text, tool calls, run state, and errors over server-sent events.
- Inspect and edit files, fetch public web text, and run approved local commands.
- Configure confirmation rules and filesystem boundaries per tool.
- Pause a run to ask the user when a decision is needed.
- Maintain explicit revisioned session notes from MaterialMind or ACP runtimes without injecting them into every prompt.
- Use local Agent Client Protocol (ACP) agents as alternative session runtimes.
- Answer ACP agent form and external-link requests in the activity timeline.
- Inspect ACP capabilities, authenticate with agent-managed methods, and import discoverable sessions.
- Route ACP text-file requests through session filesystem boundaries and edit approvals.
- Connect local stdio and remote Streamable HTTP MCP servers, including OAuth-protected servers.
- Assign MCP servers and per-tool confirmation rules to workspaces and sessions.
- Add MCP resources and prompts to a message and render MCP Apps in a constrained inline sandbox.
- Discover workspace and user-level agent skills.
- Persist workspaces, sessions, runs, state, and events in SQLite.
- Load long conversations in pages without losing live run updates.
- Download a consistent database backup and optionally remove old idle sessions automatically.

## Quick Start

```bash
npm ci --prefix web
npm run build
TEAM_OPENAI_TOKEN=your-token ./dist/materialmind
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080), add a provider and model in Settings, register a workspace, then start a session.

You need Go 1.26.5, Node.js 24, and npm 11 to build from source. Install `ripgrep` if you want the `grep` tool to be available.

## Docs

- [Using MaterialMind](docs/usage.md)
- [Development](docs/development.md)

## Security

MaterialMind is designed for single-user, local use and has no login screen. Anyone who can reach the server can use it with the permissions of the operating-system user running it. That can include file access, command execution, credentials available in the backend environment, session logs, and connected local agents.

Keep it bound to a loopback address such as `127.0.0.1`. Do not expose it directly to a public or shared network unless it is protected by authentication, HTTPS, and network-level access controls.
