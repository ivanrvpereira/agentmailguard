import type { Header } from "postal-mime";
import type { AuthResult, AuthSignals } from "./types";

const TRUSTED_AUTH_SERVERS = new Set([
  "mx.cloudflare.net",
  "email.cloudflare.net",
  "cloudflare-email.com",
]);

const AUTH_RESULT_PATTERN = /\b(spf|dkim|dmarc)\s*=\s*([a-z]+)/gi;

export function authSignalsFromHeaders(headers: Header[]): AuthSignals {
  // Cloudflare exposes Authentication-Results in raw MIME headers, not message.headers.
  const header = headers.find((candidate) => candidate.key === "authentication-results");
  return header && isTrustedAuthHeader(header.value) ? parseAuthResults(header.value) : unknownAuthSignals();
}

export function unknownAuthSignals(): AuthSignals {
  return {
    spf: "unknown",
    dkim: "unknown",
    dmarc: "unknown",
  };
}

function isTrustedAuthHeader(value: string): boolean {
  const authservId = value.split(";", 1)[0]?.trim().split(/\s+/, 1)[0]?.replace(/\.$/, "").toLowerCase();
  if (!authservId) return false;
  if (TRUSTED_AUTH_SERVERS.has(authservId)) return true;
  return [...TRUSTED_AUTH_SERVERS].some((server) => authservId.endsWith(`.${server}`));
}

function parseAuthResults(value: string): AuthSignals {
  const results: Record<keyof AuthSignals, AuthResult[]> = {
    spf: [],
    dkim: [],
    dmarc: [],
  };

  for (const match of value.matchAll(AUTH_RESULT_PATTERN)) {
    const method = match[1].toLowerCase() as keyof AuthSignals;
    results[method].push(normalizeAuthResult(match[2]));
  }

  return {
    spf: selectAuthResult(results.spf),
    dkim: selectAuthResult(results.dkim),
    dmarc: selectAuthResult(results.dmarc),
  };
}

function normalizeAuthResult(value: string): AuthResult {
  const normalized = value.toLowerCase();
  if (
    normalized === "pass" ||
    normalized === "fail" ||
    normalized === "softfail" ||
    normalized === "neutral" ||
    normalized === "none"
  ) {
    return normalized;
  }

  return "unknown";
}

function selectAuthResult(results: AuthResult[]): AuthResult {
  if (results.length === 0) return "unknown";
  if (results.includes("pass")) return "pass";
  if (results.includes("fail")) return "fail";
  if (results.includes("softfail")) return "softfail";
  if (results.includes("neutral")) return "neutral";
  if (results.includes("none")) return "none";
  return "unknown";
}
