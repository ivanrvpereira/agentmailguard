# MCP server decisions

Date: 2026-09-29. The user asked for research and an autonomous implementation, and was not available for questions. This file records every decision the orchestrator made alone, with the reason and what to change if the user disagrees. Facts come from `docs/research/mcp-go-server.md`.

## 1. New dependency: `github.com/modelcontextprotocol/go-sdk` v1.8.0

`AGENTS.md` says not to add dependencies outside the plan's categories without approval. The user asked for an MCP implementation and asked which library to use, so a protocol library is implied. The choice needs review.

- Official SDK, Tier 1 in the spec's SDK list, v1-stable since 2025-09-30 ("we won't make breaking API changes"), 2026-09-14 release.
- Serves protocol revisions 2024-11-05 through 2026-07-28 on one API. Claude Code still uses the legacy handshake for stdio servers by default; other clients may use 2026-07-28. Both work without code on our side.
- `go 1.25.0`, same as this repository. `mark3labs/mcp-go` would force `go 1.25.5` and adds `spf13/cast` plus a second JSON Schema validator. `metoro-io/mcp-golang` and `ThinkInAIXYZ/go-mcp` are stale or behind the protocol.
- Transitive modules pulled into the build: `google/jsonschema-go`, `segmentio/encoding`, `segmentio/asm`, `yosida95/uritemplate/v3`, `golang.org/x/oauth2`, `golang.org/x/sync`, `golang.org/x/sys`, `golang.org/x/time`. All pinned in `go.sum`.

To reverse: remove the `mcp` command and `internal/mcptools`, run `go mod tidy`.

## 2. Transport: stdio subcommand, not an HTTP endpoint on `serve`

`agentmailguard-go mcp` is a thin adapter. The agent's client launches it; it forwards each tool call to `POST /v1/call` through `internal/client`.

- The MCP process holds no provider credentials and no OpenRouter key. This is what the Python plan meant by "MCP server on a different host so the agent cannot reach the guard's tokens", and the Go design already had the split.
- The spec says stdio servers should take credentials from the environment and not use OAuth; the security best practices page recommends stdio for local servers.
- Claude Desktop can only reach local servers through stdio. Claude Code, Cursor, and pi all support stdio.
- An HTTP MCP endpoint on `serve` would need Origin validation, `Stateless: true` for 2026-07-28, and a bearer token. Client/server authentication is deferred in this project, so it is out of scope. The research file lists what to do when it is added.

## 3. Read-only by default, `--allow-writes` opt-in

Without `--allow-writes`, only read tools exist: `mail_list`, `mail_get`, `mail_search`, `mail_link`, `mail_folders`, `mail_categories`, `mail_organize` (dry run), `calendar_list`, `calendar_get`, `rules_list`, `auth_status`. With it, the mutation and draft tools exist as well, and `mail_organize` accepts `apply: true`.

- Agents often auto-approve MCP tool calls, and tool annotations are hints the client must treat as untrusted. A flag in the client configuration is a real control; an annotation is not.
- The CLI already exposes every operation to an agent, so the write set does not widen what an agent can do; it only requires an explicit decision.
- One flag instead of a per-tool allow list keeps the concept count low.

Alternative not taken: exposing everything and relying on `destructiveHint`. Alternative not taken: separate `--allow-drafts`; drafts never send, so they sit with the other writes.

## 4. Tools never exposed

`stats` (not needed for mail work), `cache.clear` (maintenance; clearing verdicts forces re-screening and is not an agent task), `serve`, `auth login`, `auth set-jev-key`, `auth migrate`, `auth change-password` (local host operations).

## 5. Tool names, arguments, and results

- Names use `[a-z_]` (`mail_list`, not `mail.list`). The spec allows dots, but no source confirmed that every client accepts them.
- Arguments mirror the HTTP parameters. The adapter does not re-validate what the server validates, so refusals like `invalid request` and `search needs at least one of text, from, subject, after, before` reach the agent as tool errors with the server's fixed text.
- `calendar_list` takes RFC 3339 `start`/`end` (the server's format) instead of the CLI's `today`/`+Nd` syntax, to avoid moving CLI date parsing into a shared package.
- Results are typed, so the SDK publishes `outputSchema`, validates `structuredContent`, and adds the JSON text block that legacy clients read. The types are the existing agent-facing views plus small wrappers, so the JSON an agent sees matches the CLI's JSON.
- Every tool has an optional `account` argument; `--account` on the command is the default, as in the CLI.

## 6. Annotations and Claude Code meta

Every hint is set explicitly because the spec defaults `destructiveHint` and `openWorldHint` to true. The table is in `PLAN.md`. Judgment calls: reads that return email-derived text are `openWorldHint: true` (content from arbitrary third parties); archive and move are `destructiveHint: false` (reversible); trash and organize-with-apply are `destructiveHint: true`; `draft_update` is destructive because it overwrites. `mail_trash` and organize in writes mode also set `_meta["anthropic/requiresUserInteraction"]: true`, which forces a Claude Code permission prompt. Other clients ignore it.

## 7. Untrusted content handling

The server's verdict views remain the single screening chokepoint; the adapter changes no shapes. The server `instructions` field and each tool description say that sender, subject, body, attachment names, links, and event text are untrusted data. No sampling (would send email to the host model), no elicitation (the host can auto-answer it, so it is not a confirmation control), no logging capability (deprecated in 2026-07-28; nothing is logged anyway).

## 8. Error text

Only the fixed texts the CLI already shows can reach the agent. Transport failures are `server request failed`; undecodable responses are `invalid server response`; handler panics are `operation failed`. No SDK, HTTP, or provider text.

## 9. Process

Branch `mcp-server` in `../agentmailguard-go-mcp`. Three steps (read tools and command; write tools; docs and native test). Each step: an Opus 5.5 implementation worker, an Opus 5.5 review worker, orchestrator verification, one conventional commit. `PLAN.md` and `PROGRESS.md` stay untracked as in the previous work. This file and the research file are committed under `docs/`.
