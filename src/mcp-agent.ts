import { McpAgent } from "agents/mcp";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { getEmail, getEmailSummariesByIds, listEmails, searchEmails } from "./store/d1";
import { semanticSearchIds } from "./store/vectorize";
import type { Env, RiskLevel } from "./pipeline/types";

const riskSchema = z.enum(["red", "yellow", "green"]);

export class EmailMcpAgent extends McpAgent<Env> {
  server = new McpServer({
    name: "agentmailguard",
    version: "0.1.0",
  });

  async init(): Promise<void> {
    this.server.registerTool(
      "list_emails",
      {
        description: "List processed email summaries. Bodies are never returned by this tool.",
        inputSchema: {
          limit: z.number().int().min(1).max(100).optional(),
          offset: z.number().int().min(0).optional(),
          risk_filter: riskSchema.optional(),
          since: z.string().optional(),
          sender: z.string().min(1).optional(),
        },
      },
      async ({ limit, offset, risk_filter, since, sender }) =>
        jsonResult(
          await listEmails(this.env.DB, {
            limit,
            offset,
            riskFilter: risk_filter,
            since,
            sender,
          }),
        ),
    );

    this.server.registerTool(
      "get_email",
      {
        description: "Get a processed email by id. Response is tier-gated by risk level.",
        inputSchema: {
          id: z.string().min(1),
        },
      },
      async ({ id }) => {
        const email = await getEmail(this.env.DB, id);
        return jsonResult(email ?? { error: "email not found" });
      },
    );

    this.server.registerTool(
      "search_emails",
      {
        description: "Keyword search processed email summaries.",
        inputSchema: {
          query: z.string().min(1),
          sender: z.string().min(1).optional(),
          risk_filter: riskSchema.optional(),
          since: z.string().optional(),
          limit: z.number().int().min(1).max(100).optional(),
        },
      },
      async ({ query, sender, risk_filter, since, limit }) =>
        jsonResult(
          await searchEmails(this.env.DB, {
            query,
            sender,
            riskFilter: risk_filter,
            since,
            limit,
          }),
        ),
    );

    this.server.registerTool(
      "semantic_search",
      {
        description: "Semantic search processed email summaries using Vectorize embeddings.",
        inputSchema: {
          query: z.string().min(1),
          limit: z.number().int().min(1).max(50).optional(),
          risk_filter: riskSchema.optional(),
        },
      },
      async ({ query, limit, risk_filter }) => {
        const requestedLimit = limit ?? 10;
        const vectorLimit = risk_filter ? Math.min(requestedLimit * 3, 50) : requestedLimit;
        const ids = await semanticSearchIds(this.env.AI, this.env.VECTORS, query, vectorLimit);
        const summaries = await getEmailSummariesByIds(this.env.DB, ids, risk_filter);
        return jsonResult(summaries.slice(0, requestedLimit));
      },
    );
  }
}

function jsonResult(value: unknown) {
  return {
    content: [
      {
        type: "text" as const,
        text: JSON.stringify(value, null, 2),
      },
    ],
  };
}
