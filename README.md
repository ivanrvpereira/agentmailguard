# AgentMailGuard

Cloudflare-native email gateway for AI agents.

## Setup

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

Apply the schema:

```bash
npm run db:migrate:remote
```

Deploy:

```bash
npm run deploy
```

Then in Cloudflare Email Routing for `agentmailguard.dev`, route inbound mail to this Worker.

## MCP

The Worker exposes:

- `/sse` — SSE MCP endpoint
- `/mcp` — streamable HTTP MCP endpoint

Tools:

- `list_emails`
- `get_email`
- `search_emails`
- `semantic_search`
