#!/usr/bin/env node
/// <reference types="node" />

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const DEFAULT_MCP_URL = "http://localhost:8787/mcp";
const RISK_LEVELS = new Set(["red", "yellow", "green"]);

type Command = "list" | "get" | "search";

interface ParsedArgs {
  command?: Command;
  positional: string[];
  options: Record<string, string | boolean>;
}

async function main(): Promise<void> {
  const parsed = parseArgs(process.argv.slice(2));

  if (!parsed.command || hasFlag(parsed, "help")) {
    printUsage();
    process.exit(hasFlag(parsed, "help") ? 0 : 1);
  }

  const mcpUrl = getStringOption(parsed, "url") ?? process.env.AGENTMAILGUARD_MCP_URL ?? DEFAULT_MCP_URL;
  const token = getStringOption(parsed, "token") ?? process.env.AGENTMAILGUARD_TOKEN;

  if (!token) {
    throw new CliError("Missing token. Set AGENTMAILGUARD_TOKEN or pass --token.");
  }

  const result = await callAgentMailTool(mcpUrl, token, parsed.command, toolArguments(parsed));
  console.log(JSON.stringify(result, null, 2));
}

async function callAgentMailTool(
  mcpUrl: string,
  token: string,
  command: Command,
  args: Record<string, unknown>,
): Promise<unknown> {
  const client = new Client({ name: "agentmailguard-cli", version: "0.1.0" });
  const transport = new StreamableHTTPClientTransport(new URL(mcpUrl), {
    requestInit: {
      headers: {
        authorization: `Bearer ${token}`,
      },
    },
  });

  try {
    await client.connect(transport);
    const response = await client.callTool({ name: toolName(command), arguments: args });
    return parseToolResponse(response);
  } finally {
    await client.close();
  }
}

function toolName(command: Command): string {
  switch (command) {
    case "list":
      return "list_emails";
    case "get":
      return "get_email";
    case "search":
      return "search_emails";
  }
}

function toolArguments(parsed: ParsedArgs): Record<string, unknown> {
  switch (parsed.command) {
    case "list":
      return compact({
        limit: getIntegerOption(parsed, "limit", 1),
        offset: getIntegerOption(parsed, "offset", 0),
        risk_filter: getRiskOption(parsed),
        since: getStringOption(parsed, "since"),
        sender: getStringOption(parsed, "sender"),
      });
    case "get": {
      const id = parsed.positional[0];
      if (!id) throw new CliError("Missing email id.");
      return { id };
    }
    case "search": {
      const query = parsed.positional.join(" ").trim();
      if (!query) throw new CliError("Missing search query.");
      return compact({
        query,
        sender: getStringOption(parsed, "sender"),
        risk_filter: getRiskOption(parsed),
        since: getStringOption(parsed, "since"),
        limit: getIntegerOption(parsed, "limit", 1),
      });
    }
    default:
      throw new CliError("Unknown command.");
  }
}

function parseToolResponse(response: unknown): unknown {
  if (!isRecord(response)) return response;

  const content = Array.isArray(response.content) ? response.content : [];
  const textPart = content.find((part): part is { type: "text"; text: string } => {
    return isRecord(part) && part.type === "text" && typeof part.text === "string";
  });

  if (!textPart) return response;

  try {
    return JSON.parse(textPart.text) as unknown;
  } catch {
    return textPart.text;
  }
}

function parseArgs(args: string[]): ParsedArgs {
  const parsed: ParsedArgs = { positional: [], options: {} };

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];

    if (!parsed.command && isCommand(arg)) {
      parsed.command = arg;
      continue;
    }

    if (arg.startsWith("--")) {
      const [rawName, rawValue] = arg.slice(2).split("=", 2);
      const name = normalizeOption(rawName);
      const next = args[index + 1];

      if (rawValue !== undefined) {
        parsed.options[name] = rawValue;
      } else if (!next || next.startsWith("--")) {
        parsed.options[name] = true;
      } else {
        parsed.options[name] = next;
        index += 1;
      }
      continue;
    }

    parsed.positional.push(arg);
  }

  return parsed;
}

function isCommand(value: string): value is Command {
  return value === "list" || value === "get" || value === "search";
}

function getStringOption(parsed: ParsedArgs, name: string): string | undefined {
  const value = parsed.options[name];
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function getIntegerOption(parsed: ParsedArgs, name: string, min: number): number | undefined {
  const value = getStringOption(parsed, name);
  if (value === undefined) return undefined;

  const numberValue = Number(value);
  if (!Number.isInteger(numberValue) || numberValue < min) {
    throw new CliError(`--${name} must be an integer greater than or equal to ${min}.`);
  }

  return numberValue;
}

function getRiskOption(parsed: ParsedArgs): string | undefined {
  const value =
    getStringOption(parsed, "risk") ?? getStringOption(parsed, "risk-filter") ?? getStringOption(parsed, "risk_filter");
  if (value === undefined) return undefined;

  if (!RISK_LEVELS.has(value)) {
    throw new CliError("--risk must be one of: red, yellow, green.");
  }

  return value;
}

function hasFlag(parsed: ParsedArgs, name: string): boolean {
  return parsed.options[name] === true;
}

function normalizeOption(name: string): string {
  return name.trim().toLowerCase();
}

function compact(values: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(values).filter(([, value]) => value !== undefined));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function printUsage(): void {
  console.log(`Usage:
  agentmail list [--limit 25] [--offset 0] [--risk red|yellow|green] [--since ISO_DATE] [--sender TEXT]
  agentmail get EMAIL_ID
  agentmail search QUERY [--limit 25] [--risk red|yellow|green] [--since ISO_DATE] [--sender TEXT]

Config:
  AGENTMAILGUARD_MCP_URL   MCP endpoint URL, defaults to ${DEFAULT_MCP_URL}
  AGENTMAILGUARD_TOKEN     MCP shared secret

Options:
  --url URL                Override MCP endpoint URL
  --token TOKEN            Override MCP token
  --help                   Show this help
`);
}

class CliError extends Error {}

main().catch((error: unknown) => {
  if (error instanceof CliError) {
    console.error(`Error: ${error.message}`);
  } else if (error instanceof Error) {
    console.error(error.message);
  } else {
    console.error(String(error));
  }

  process.exit(1);
});
