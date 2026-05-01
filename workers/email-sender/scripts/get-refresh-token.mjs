/**
 * One-time script to get a Gmail OAuth2 refresh token.
 * Run: node scripts/get-refresh-token.mjs
 *
 * Prerequisites:
 *  1. Go to https://console.cloud.google.com
 *  2. Create a project → enable Gmail API
 *  3. Create OAuth2 credentials (type: Desktop app)
 *  4. Set CLIENT_ID and CLIENT_SECRET below
 */

import readline from "readline";
import https from "https";

const CLIENT_ID = "YOUR_CLIENT_ID";
const CLIENT_SECRET = "YOUR_CLIENT_SECRET";
const REDIRECT_URI = "urn:ietf:wg:oauth:2.0:oob";
const SCOPE = "https://www.googleapis.com/auth/gmail.send";

const authUrl =
  `https://accounts.google.com/o/oauth2/v2/auth` +
  `?client_id=${CLIENT_ID}` +
  `&redirect_uri=${encodeURIComponent(REDIRECT_URI)}` +
  `&response_type=code` +
  `&scope=${encodeURIComponent(SCOPE)}` +
  `&access_type=offline` +
  `&prompt=consent`;

console.log("\n1. Open this URL in your browser:\n");
console.log(authUrl);
console.log("\n2. Approve access, then paste the code shown below:\n");

const rl = readline.createInterface({ input: process.stdin, output: process.stdout });

rl.question("Paste the authorization code: ", (code) => {
  rl.close();

  const body = new URLSearchParams({
    code: code.trim(),
    client_id: CLIENT_ID,
    client_secret: CLIENT_SECRET,
    redirect_uri: REDIRECT_URI,
    grant_type: "authorization_code",
  }).toString();

  const req = https.request(
    {
      hostname: "oauth2.googleapis.com",
      path: "/token",
      method: "POST",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "Content-Length": Buffer.byteLength(body),
      },
    },
    (res) => {
      let data = "";
      res.on("data", (chunk) => (data += chunk));
      res.on("end", () => {
        const parsed = JSON.parse(data);
        if (parsed.refresh_token) {
          console.log("\nSuccess! Run these commands to store your secrets:\n");
          console.log(`npx wrangler secret put GMAIL_CLIENT_ID`);
          console.log(`  → paste: ${CLIENT_ID}\n`);
          console.log(`npx wrangler secret put GMAIL_CLIENT_SECRET`);
          console.log(`  → paste: ${CLIENT_SECRET}\n`);
          console.log(`npx wrangler secret put GMAIL_REFRESH_TOKEN`);
          console.log(`  → paste: ${parsed.refresh_token}\n`);
          console.log(`npx wrangler secret put RECIPIENTS`);
          console.log(`  → paste: comma-separated recipient emails\n`);
        } else {
          console.error("\nError:", JSON.stringify(parsed, null, 2));
        }
      });
    }
  );

  req.write(body);
  req.end();
});
