![Uploading image.png…]()
# AgentMailGuard

Agents accessing your mailbox is a risk. Prompt injection hidden in email bodies, credential exposure from raw IMAP access, and unrestricted read of sensitive threads are real attack surfaces the moment an AI agent connects to your inbox. AgentMailGuard is a firewall between your mail and your agents.

It receives inbound mail through Cloudflare Email Routing, runs it through a deterministic cleaning and threat detection pipeline, classifies risk with Workers AI, and exposes only the processed output through authenticated MCP tools — never raw mail, never mailbox credentials.

> Built for the **Cloudflare AgentsDay Hackathon** — "Build a Personal Agent that Automates a Meaningful Task in your Life".

## How it works

```text
External sender
  → Cloudflare Email Routing
  → AgentMailGuard Worker
      → parse (postal-mime)
      → clean (strip HTML, neutralize injections, normalize text)
      → detect (prompt injection, opaque payloads, homoglyphs, evasion)
      → classify (Workers AI → red / yellow / green)
      → extract entities (emails, URLs, dates, names, phones)
      → store metadata + cleaned body in D1
      → embed in Vectorize
  → MCP tools (authenticated)
  → AI agent
```

Key invariants:

- Raw email is transient and never written to storage.
- Email-derived text is always untrusted, even after cleaning.
- Agents never hold mailbox credentials; mail arrives via MX routing.
- Risk level gates which fields an agent can read.
- Missing trust signals never increase trust.

## Risk-gated access

`get_email` returns different fields based on risk level:

| Field | Red | Yellow | Green |
|---|:---:|:---:|:---:|
| id, sender, subject, received_at | yes | yes | yes |
| risk_level, risk_reasons, labels | yes | yes | yes |
| recipients | no | yes | yes |
| entities | no | yes | yes |
| threat_flags | no | yes | yes |
| cleaned_body | no | no | yes |

## MCP tools

Endpoints:

- `/sse` — SSE MCP transport
- `/mcp` — streamable HTTP MCP transport

Pass the shared secret as either:

```text
Authorization: Bearer <MCP_SHARED_SECRET>
x-agentmailguard-token: <MCP_SHARED_SECRET>
```

Available tools:

- `list_emails` — paginated list of processed email summaries
- `get_email` — single email by id, risk-gated
- `search_emails` — keyword search over summaries
- `semantic_search` — Vectorize embedding search

## Email sender (test worker)

`workers/email-sender/` is a companion Cloudflare Worker that continuously sends realistic phishing and safe test emails at random 1–30 second intervals via the Gmail API. Use it to drive test traffic through the pipeline.

```bash
# start
curl -X POST https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main/start
# stop
curl -X POST https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main/stop
# status
curl https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main
```

See `workers/email-sender/README.md` for setup instructions.

## Setup

Install and authenticate:

```bash
npm install
npx wrangler login
```

Create Cloudflare resources:

```bash
npx wrangler d1 create agentmail
npx wrangler vectorize create email-embeddings --dimensions=768 --metric=cosine
```

Copy the D1 `database_id` into `wrangler.toml`, then:

```bash
npx wrangler secret put MCP_SHARED_SECRET
npm run db:migrate:remote
npm run deploy
```

Configure Cloudflare Email Routing to route inbound mail to this Worker.

## Development

```bash
npm run dev           # local Worker
npm run typecheck     # TypeScript check
npm run cf-typegen    # regenerate Cloudflare binding types
npm run db:migrate:local
```

## Project structure

```text
src/
  index.ts              Worker entry — MCP auth, fetch/email handlers
  mcp-agent.ts          MCP Durable Object and tool definitions
  pipeline/
    process.ts          Pipeline orchestrator
    cleaner.ts          Deterministic text cleaning
    detector.ts         Heuristic threat detection
    classifier.ts       Workers AI classification and fallback policy
    extractor.ts        Entity extraction
    types.ts            Shared types and Cloudflare env bindings
  store/
    d1.ts               D1 reads/writes and risk-gated output
    vectorize.ts        Embedding and semantic search helpers
migrations/             D1 schema migrations
workers/
  email-sender/         Standalone test email sender Worker
wrangler.toml           Cloudflare bindings and deployment config
```

## Cloudflare bindings

| Binding | Type | Purpose |
|---|---|---|
| `AI` | Workers AI | Email classification |
| `DB` | D1 | Processed email storage |
| `VECTORS` | Vectorize | Semantic search embeddings |
| `MCP_AGENT` | Durable Object | MCP agent state |
| `MCP_SHARED_SECRET` | Secret | MCP endpoint auth |
