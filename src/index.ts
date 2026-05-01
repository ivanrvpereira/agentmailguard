import { EmailMcpAgent } from "./mcp-agent";
import { processInboundEmail } from "./pipeline/process";
import type { Env } from "./pipeline/types";

export { EmailMcpAgent };

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/sse" || url.pathname.startsWith("/sse/")) {
      return EmailMcpAgent.serveSSE("/sse").fetch(request, env, ctx);
    }

    if (url.pathname === "/mcp") {
      return EmailMcpAgent.serve("/mcp").fetch(request, env, ctx);
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
