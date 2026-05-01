# agentmailguard-email-sender

Companion Worker that continuously sends realistic phishing and safe test emails at random 1–30 second intervals via the Gmail API. Drives test traffic through the AgentMailGuard pipeline.

## Setup

### 1. Install

```bash
npm install
```

### 2. Get a Gmail OAuth2 refresh token

1. Go to [Google Cloud Console](https://console.cloud.google.com)
2. Create a project → enable **Gmail API**
3. Create OAuth2 credentials (type: **Desktop app**)
4. Add your Gmail address as a test user in the OAuth consent screen
5. Fill in `CLIENT_ID` and `CLIENT_SECRET` in `scripts/get-refresh-token.mjs`
6. Run:

```bash
node scripts/get-refresh-token.mjs
```

### 3. Store secrets

```bash
npx wrangler secret put GMAIL_CLIENT_ID
npx wrangler secret put GMAIL_CLIENT_SECRET
npx wrangler secret put GMAIL_REFRESH_TOKEN
npx wrangler secret put RECIPIENTS   # comma-separated: a@example.com,b@example.com
```

Set `RECIPIENTS` to the address that Cloudflare Email Routing forwards into AgentMailGuard.

### 4. Deploy

```bash
npm run deploy
# or from the repo root:
npm run deploy:sender
```

## Usage

```bash
# start
curl -X POST https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main/start

# stop
curl -X POST https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main/stop

# status
curl https://agentmailguard-email-sender.<account>.workers.dev/agents/email-sender-agent/main
```

## Dataset

`Phishing_validation_emails.csv` contains 2000 labeled emails (Safe / Phishing). Regenerate `src/emailData.json` from it with:

```bash
node scripts/convert-csv.mjs
```
