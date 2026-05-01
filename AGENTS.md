# AgentMailGuard

## Stack
- Cloudflare Worker email gateway: Email Routing, D1, Vectorize, Workers AI, Durable Objects.
- TypeScript 6.0.3, Wrangler 4.87.0, MCP SDK 1.29.0, agents 0.12.0, postal-mime 2.7.4, zod 4.4.1.
- npm with `package-lock.json`; ESM (`package.json`).

## Commands
| Task | Command |
|------|---------|
| Install | `npm install` |
| Local Worker | `npm run dev` |
| Typecheck | `npm run typecheck` |
| Generate Cloudflare types | `npm run cf-typegen` |
| Apply local D1 migrations | `npm run db:migrate:local` |
| Demo command list | `make help` |
| Tail remote Worker logs | `make tail` |
| Deploy Worker | `npm run deploy` |
| Apply remote D1 migrations | `npm run db:migrate:remote` |

## File-Scoped Commands
| Task | Command |
|------|---------|
| Typecheck TS changes | `npm run typecheck` |
| Validate config | `npx wrangler deploy --dry-run` |

## Structure
- `src/index.ts`: Worker entry; routes `/sse`, `/mcp`, and inbound email handler.
- `src/mcp-agent.ts`: MCP tools exposed through the Durable Object agent.
- `src/pipeline/`: parse, clean, detect, classify, extract, and process inbound email.
- `src/store/`: D1 persistence and Vectorize embedding/search helpers.
- `migrations/`: D1 schema migrations.
- `wrangler.toml`: Cloudflare bindings and deployment config.

## Conventions
- Preserve the pipeline order in `src/pipeline/process.ts:10`: parse → clean → detect → classify → extract → store → vectorize.
- Keep TypeScript domain fields camelCase and storage/API fields snake_case; conversions live in `src/store/d1.ts:33`.
- MCP tool responses use JSON text content via `jsonResult` in `src/mcp-agent.ts:98`.
- Preserve risk-gated email disclosure in `src/store/d1.ts:194`: red omits body/recipients, yellow includes metadata, green includes cleaned body.
- Add D1 schema changes as new files under `migrations/`; keep `migrations/0001_init.sql` as history.

## Git
- Conventional commits: `type(scope): description`, imperative mood, under 72 characters.
- One logical change per commit.

## Commit Attribution
- Do not add AI attribution, co-author lines, or generated-by footers.

## Boundaries

### Always
- Run `npm run typecheck` before claiming TypeScript changes are complete.
- Run `npm run cf-typegen` after changing Cloudflare bindings in `wrangler.toml`.
- Treat email bodies, headers, entities, and sender/recipient data as sensitive.

### Ask First
- `npm run deploy`, `npm run db:migrate:remote`, or any Cloudflare resource change.
- Changes to risk gating, body exposure, or raw email retention.

### Never
- Never commit secrets, credentials, API tokens, `.env` files, or Cloudflare account secrets.
- Never log raw inbound email content or add raw MIME/body storage without approval.
