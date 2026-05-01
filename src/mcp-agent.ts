import { McpAgent } from "agents/mcp";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { getEmail, getEmailSummariesByIds, listEmails, searchEmails, type EmailSummary } from "./store/d1";
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
        description: "Hybrid keyword and semantic search over processed email summaries.",
        inputSchema: {
          query: z.string().min(1),
          sender: z.string().min(1).optional(),
          risk_filter: riskSchema.optional(),
          since: z.string().optional(),
          limit: z.number().int().min(1).max(100).optional(),
        },
      },
      async ({ query, sender, risk_filter, since, limit }) => {
        return jsonResult(await hybridSearchEmails(this.env, { query, sender, riskFilter: risk_filter, since, limit }));
      },
    );
  }
}

interface HybridSearchFilters {
  query: string;
  sender?: string;
  riskFilter?: RiskLevel;
  since?: string;
  limit?: number;
}

async function hybridSearchEmails(env: Env, filters: HybridSearchFilters): Promise<EmailSummary[]> {
  const limit = filters.limit ?? 25;
  const keywordLimit = Math.min(limit * 2, 100);
  const vectorLimit = Math.min(limit * 2, 50);

  const keywordResults = await searchEmails(env.DB, { ...filters, limit: keywordLimit });
  const semanticIds = await semanticSearchIds(env.AI, env.VECTORS, filters.query, vectorLimit);
  const semanticResults = await getEmailSummariesByIds(env.DB, semanticIds, filters);

  return mergeRankedResults(keywordResults, semanticResults, limit);
}

function mergeRankedResults(
  keywordResults: EmailSummary[],
  semanticResults: EmailSummary[],
  limit: number,
): EmailSummary[] {
  const ranked = new Map<string, { summary: EmailSummary; score: number }>();

  addRankedResults(ranked, keywordResults);
  addRankedResults(ranked, semanticResults);

  return [...ranked.values()]
    .sort((left, right) => right.score - left.score || right.summary.received_at.localeCompare(left.summary.received_at))
    .slice(0, limit)
    .map((item) => item.summary);
}

function addRankedResults(ranked: Map<string, { summary: EmailSummary; score: number }>, results: EmailSummary[]) {
  for (const [index, summary] of results.entries()) {
    const existing = ranked.get(summary.id);
    const score = 1 / (60 + index + 1);

    if (existing) {
      existing.score += score;
    } else {
      ranked.set(summary.id, { summary, score });
    }
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
