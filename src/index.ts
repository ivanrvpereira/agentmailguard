import { EmailMcpAgent } from "./mcp-agent";
import { processInboundEmail } from "./pipeline/process";
import type { Env } from "./pipeline/types";

export { EmailMcpAgent };

async function authorizeMcpRequest(request: Request, env: Env): Promise<Response | undefined> {
  if (!env.MCP_SHARED_SECRET) {
    return Response.json({ error: "MCP authentication is not configured" }, { status: 503 });
  }

  const token = bearerToken(request) ?? request.headers.get("x-agentmailguard-token");
  if (!token || !(await secretsMatch(token, env.MCP_SHARED_SECRET))) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  return undefined;
}

function bearerToken(request: Request): string | undefined {
  const header = request.headers.get("authorization");
  const match = header?.match(/^Bearer\s+(.+)$/i);
  return match?.[1];
}

async function secretsMatch(actual: string, expected: string): Promise<boolean> {
  const [actualDigest, expectedDigest] = await Promise.all([sha256(actual), sha256(expected)]);
  return actualDigest === expectedDigest;
}

async function sha256(value: string): Promise<string> {
  const bytes = new TextEncoder().encode(value);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/sse" || url.pathname.startsWith("/sse/")) {
      const authResponse = await authorizeMcpRequest(request, env);
      if (authResponse) return authResponse;

      return EmailMcpAgent.serveSSE("/sse", { binding: "MCP_AGENT" }).fetch(request, env, ctx);
    }

    if (url.pathname === "/mcp" || url.pathname.startsWith("/mcp/")) {
      const authResponse = await authorizeMcpRequest(request, env);
      if (authResponse) return authResponse;

      return EmailMcpAgent.serve("/mcp", { binding: "MCP_AGENT" }).fetch(request, env, ctx);
    }

    return Response.json({
      name: "agentmailguard",
      mcp: "/sse",
      status: "ok",
    });
  },

  async email(message, env): Promise<void> {
    await processInboundEmail(message, env);
  },
} satisfies ExportedHandler<Env>;
