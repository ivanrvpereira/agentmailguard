# AgentMailGuard

AgentMailGuard is a Cloudflare-native email gateway for AI agents. It receives inbound mail through Cloudflare Email Routing, cleans and classifies the message, stores only processed content, and exposes read-only MCP tools for agent access.

The goal is to let agents search and read email without giving them mailbox credentials or raw mailbox access.

## Core idea

```text
External sender
  -> Cloudflare Email Routing
  -> Email Worker
  -> deterministic cleaner
  -> threat detector
  -> Workers AI classifier
  -> D1 + Vectorize
  -> authenticated MCP tools
  -> AI agent
```

Key invariants:

- Raw email is transient and is never written to D1 or Vectorize.
- Email-derived text is always untrusted, even after cleaning.
- Agents never hold mailbox credentials; mail arrives via MX routing.
- Risk level controls which fields an agent can read.
- Missing trust signals never increase trust.

## Current MVP

The current implementation is the hackathon/store-only version of the design:

- Inbound email is parsed with `postal-mime`.
- Cleaned email bodies are stored in D1 as text.
- Embeddings are stored in Cloudflare Vectorize for semantic search.
- Classification uses Workers AI with a conservative fallback.
- MCP endpoints are protected by a shared secret.
- There is no cleaned re-delivery to a human inbox yet.
- There is no R2 body storage or attachment scanning yet.

## Pipeline

1. **Parse** inbound email from Cloudflare Email Routing.
2. **Clean** text deterministically:
   - strip HTML, scripts, styles, and comments
   - decode HTML entities
   - normalize Unicode
   - remove invisible/control characters
   - neutralize markdown links/images, data URIs, code fences, base64, and hex blobs
   - truncate subject/body
3. **Detect threats** using heuristic flags for prompt injection, residual URLs, data URIs, opaque payloads, homoglyphs, and Zalgo-style evasion.
4. **Classify risk** with Workers AI into `red`, `yellow`, or `green`.
5. **Extract entities** such as emails, URLs, dates, names, and phone numbers.
6. **Store** metadata, cleaned body, classification, threat flags, and entities in D1.
7. **Embed** subject + cleaned body in Vectorize on a best-effort basis.

## Risk-gated access

`get_email` returns different fields based on the stored risk level:

| Field | Red | Yellow | Green |
|---|:---:|:---:|:---:|
| id, sender, subject, received_at | yes | yes | yes |
| risk_level, risk_reasons, labels | yes | yes | yes |
| recipients | no | yes | yes |
| entities | no | yes | yes |
| threat_flags | no | yes | yes |
| cleaned_body | no | no | yes |

## MCP tools

The Worker exposes authenticated MCP endpoints:

- `/sse` — SSE MCP endpoint
- `/mcp` — streamable HTTP MCP endpoint

Tools:

- `list_emails`
- `get_email`
- `search_emails`
- `semantic_search`

Pass the shared secret as either:

```text
Authorization: Bearer <MCP_SHARED_SECRET>
x-agentmailguard-token: <MCP_SHARED_SECRET>
```

## Cloudflare resources

Required bindings:

- `AI` — Workers AI
- `DB` — D1 database named `agentmail`
- `VECTORS` — Vectorize index named `email-embeddings`
- `MCP_AGENT` — Durable Object for the MCP agent
- `MCP_SHARED_SECRET` — Worker secret for MCP authentication

## Setup

Install dependencies and authenticate Wrangler:

```bash
npm install
npx wrangler login
```

Create Cloudflare resources:

```bash
npx wrangler d1 create agentmail
npx wrangler vectorize create email-embeddings --dimensions=768 --metric=cosine
```

Copy the D1 `database_id` into `wrangler.toml`.

Set the MCP shared secret:

```bash
npx wrangler secret put MCP_SHARED_SECRET
```

Apply D1 migrations:

```bash
npm run db:migrate:remote
```

Deploy:

```bash
npm run deploy
```

Then configure Cloudflare Email Routing for `agentmailguard.dev` and route inbound mail to this Worker.

## Development

Run the Worker locally:

```bash
npm run dev
```

Typecheck:

```bash
npm run typecheck
```

Generate Cloudflare binding types:

```bash
npm run cf-typegen
```

Apply local migrations:

```bash
npm run db:migrate:local
```

## Project structure

```text
src/
  index.ts              Worker entry, MCP auth, fetch/email handlers
  mcp-agent.ts          MCP tool definitions
  pipeline/
    process.ts          Pipeline orchestrator
    cleaner.ts          Deterministic text cleaning
    detector.ts         Threat detection
    classifier.ts       Workers AI classification and fallback policy
    extractor.ts        Entity extraction
    types.ts            Shared types and Cloudflare env bindings
  store/
    d1.ts               D1 reads/writes and risk-gated output
    vectorize.ts        Embedding and semantic search helpers
migrations/             D1 schema migrations
wrangler.toml           Cloudflare Worker bindings
```

## Data model

D1 stores:

- email metadata
- cleaned body
- recipients as JSON text
- risk level and reasons
- labels and threat flags
- authentication signal fields
- Vectorize status/error fields
- extracted entities

Vectorize stores embeddings keyed as `email:<email_id>` with `email_id` metadata.

## Design references

The design specs live in `specs/`:

- `specs/2026-05-01-agentmail-hackathon-design.md`
- `specs/2026-05-01-cloudflare-email-gateway.md`
