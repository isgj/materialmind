# MaterialMind frontend

Angular Material frontend for MaterialMind. Run frontend tasks from the repository root so the same commands work locally and in CI.

## Development

```bash
npm ci --prefix web
npm run backend:dev
npm run frontend:dev
```

The Angular development server proxies `/api` to the Go backend.

## Styling

Use Angular Material and component-scoped SCSS. Global theme tokens, locally bundled Inter and JetBrains Mono fonts, and Material Symbols are defined in `src/styles.scss`. Tailwind is not part of the project.

## Build and test

```bash
npm test
npm run build
npm run verify
```

The production frontend is written to `internal/webui/dist/browser`. The root build then compiles the Go binary with the `embed_frontend` build tag.
