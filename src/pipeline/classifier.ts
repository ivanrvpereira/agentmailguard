import type { Ai } from "@cloudflare/workers-types";
import type { AuthSignals, Classification, CleanedEmail, RiskLevel } from "./types";

interface AiClassificationResponse {
  risk_level?: RiskLevel;
  risk_reasons?: string[];
  labels?: string[];
}

export async function classifyEmail(
  ai: Ai,
  email: CleanedEmail,
  threatFlags: string[],
): Promise<Classification> {
  try {
    const response = await ai.run("@cf/meta/llama-3.1-8b-instruct", {
      messages: [
        {
          role: "system",
          content:
            "Classify email risk for an AI-agent email gateway. Return only compact JSON with risk_level, risk_reasons, labels. risk_level must be red, yellow, or green. Missing auth is unknown and must not increase trust.",
        },
        {
          role: "user",
          content: JSON.stringify({
            auth: email.auth,
            threat_flags: threatFlags,
            sender: email.sender.address,
            subject: email.subject,
            body_snippet: email.body.slice(0, 2000),
          }),
        },
      ],
    });

    const parsed = parseAiResponse(response);
    if (parsed) return parsed;
  } catch (error) {
    console.warn("Workers AI classification failed", error);
  }

  return fallbackClassification(email.auth, threatFlags);
}

export function fallbackClassification(auth: AuthSignals, threatFlags: string[]): Classification {
  const reasons: string[] = [];

  if (auth.spf !== "pass") reasons.push(`SPF ${auth.spf}`);
  if (auth.dkim !== "pass") reasons.push(`DKIM ${auth.dkim}`);
  if (auth.dmarc !== "pass") reasons.push(`DMARC ${auth.dmarc}`);
  if (threatFlags.length > 0) reasons.push(...threatFlags.map((flag) => `threat: ${flag}`));

  if (hasAuthFailure(auth) || threatFlags.length >= 2) {
    return {
      riskLevel: "red",
      riskReasons: reasons.length > 0 ? reasons : ["deterministic high-risk fallback"],
      labels: ["needs_review"],
    };
  }

  if (hasUnknownAuth(auth) || threatFlags.length === 1) {
    return {
      riskLevel: "yellow",
      riskReasons: reasons.length > 0 ? reasons : ["external or partially authenticated sender"],
      labels: [],
    };
  }

  return {
    riskLevel: "green",
    riskReasons: ["authenticated sender", "no threat flags"],
    labels: [],
  };
}

function parseAiResponse(response: unknown): Classification | undefined {
  const text = extractText(response);
  if (!text) return undefined;

  const json = text.match(/\{[\s\S]*\}/)?.[0];
  if (!json) return undefined;

  const parsed = JSON.parse(json) as AiClassificationResponse;
  if (!isRiskLevel(parsed.risk_level)) return undefined;

  return {
    riskLevel: parsed.risk_level,
    riskReasons: Array.isArray(parsed.risk_reasons) ? parsed.risk_reasons.map(String) : [],
    labels: Array.isArray(parsed.labels) ? parsed.labels.map(String) : [],
  };
}

function extractText(response: unknown): string | undefined {
  if (typeof response === "string") return response;
  if (!response || typeof response !== "object") return undefined;

  const record = response as Record<string, unknown>;
  if (typeof record.response === "string") return record.response;
  if (typeof record.result === "string") return record.result;

  return undefined;
}

function isRiskLevel(value: unknown): value is RiskLevel {
  return value === "red" || value === "yellow" || value === "green";
}

function hasAuthFailure(auth: AuthSignals): boolean {
  return auth.spf === "fail" || auth.dkim === "fail" || auth.dmarc === "fail";
}

function hasUnknownAuth(auth: AuthSignals): boolean {
  return auth.spf !== "pass" || auth.dkim !== "pass" || auth.dmarc !== "pass";
}
