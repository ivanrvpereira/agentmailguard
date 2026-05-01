# Demo emails

Fictional `.eml` fixtures for rehearsing the presentation flow.

## Local send

In one terminal:

```bash
make migrate-local
make dev
```

In another terminal:

```bash
make demo-emails-local
make db-recent-local LIMIT=10
make db-entities-local LIMIT=20
```

Send one fixture:

```bash
make demo-email-local FILE=demo/emails/003-prompt-injection.eml
```

## Scenarios

- `001-team-standup.eml` — benign operational email with dates, a contact, and a phone number. Local fixture sends do not include Cloudflare `Authentication-Results`, so this appears yellow locally; real authenticated mail can appear green.
- `002-vendor-invoice.eml` — normal invoice with an HTML link. Shows HTML stripping, URL extraction, and conservative `residual_url` flagging.
- `003-prompt-injection.eml` — obvious instruction override attempt. Should classify as red and hide body/recipients through MCP `get_email`.
- `004-obfuscated-payload.eml` — data URI, long hex payload, and fake transcript markers. Should classify as red and show cleanup/threat detection.

Note: green requires trusted Cloudflare `Authentication-Results`: DMARC pass and either SPF or DKIM pass.
