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

    return enforceRiskPolicy(parseAiResponse(response), email.auth, threatFlags);
  } catch (error) {
    return failClosedClassification(email.auth, threatFlags, error);
  }
}

export function fallbackClassification(auth: AuthSignals, threatFlags: string[]): Classification {
  const reasons: string[] = [];

  if (!hasTrustedAuth(auth)) reasons.push(`auth ${authSummary(auth)}`);
  if (threatFlags.length > 0) reasons.push(...threatFlags.map((flag) => `threat: ${flag}`));

  if (hasAuthFailure(auth) || threatFlags.length >= 2) {
    return {
      riskLevel: "red",
      riskReasons: reasons.length > 0 ? reasons : ["deterministic high-risk fallback"],
      labels: ["needs_review"],
    };
  }

  if (!hasTrustedAuth(auth) || threatFlags.length === 1) {
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

function failClosedClassification(auth: AuthSignals, threatFlags: string[], error: unknown): Classification {
  const fallback = fallbackClassification(auth, threatFlags);

  if (fallback.riskLevel === "green") {
    return {
      riskLevel: "yellow",
      riskReasons: ["Workers AI classification failed", errorMessage(error)],
      labels: ["classification_failed"],
    };
  }

  return {
    ...fallback,
    riskReasons: [...fallback.riskReasons, "Workers AI classification failed", errorMessage(error)],
    labels: [...fallback.labels, "classification_failed"],
  };
}

function enforceRiskPolicy(
  classification: Classification,
  auth: AuthSignals,
  threatFlags: string[],
): Classification {
  if (hasAuthFailure(auth) || threatFlags.length >= 2) {
    return {
      ...classification,
      riskLevel: "red",
      riskReasons: [...classification.riskReasons, "policy: auth failure or multiple threat flags"],
    };
  }

  if (classification.riskLevel === "green" && (!hasTrustedAuth(auth) || threatFlags.length > 0)) {
    return {
      ...classification,
      riskLevel: "yellow",
      riskReasons: [...classification.riskReasons, "policy: green requires trusted auth and no threat flags"],
    };
  }

  return classification;
}

function parseAiResponse(response: unknown): Classification {
  const text = extractText(response);
  const json = text.match(/\{[\s\S]*\}/)?.[0];
  if (!json) throw new Error("Workers AI response did not contain JSON");

  const parsed = JSON.parse(json) as AiClassificationResponse;
  if (!isRiskLevel(parsed.risk_level)) throw new Error("Workers AI response contained an invalid risk_level");

  return {
    riskLevel: parsed.risk_level,
    riskReasons: readStringArray(parsed.risk_reasons, "risk_reasons"),
    labels: readStringArray(parsed.labels, "labels"),
  };
}

function extractText(response: unknown): string {
  if (typeof response === "string") return response;
  if (!response || typeof response !== "object") throw new Error("Workers AI response was not an object");

  const record = response as Record<string, unknown>;
  if (typeof record.response === "string") return record.response;
  if (typeof record.result === "string") return record.result;

  throw new Error("Workers AI response did not include text output");
}

function readStringArray(value: unknown, field: string): string[] {
  if (!Array.isArray(value)) throw new Error(`Workers AI response ${field} was not an array`);
  return value.map(String);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isRiskLevel(value: unknown): value is RiskLevel {
  return value === "red" || value === "yellow" || value === "green";
}

function hasAuthFailure(auth: AuthSignals): boolean {
  return auth.spf === "fail" || auth.dkim === "fail" || auth.dmarc === "fail";
}

function hasTrustedAuth(auth: AuthSignals): boolean {
  return auth.dmarc === "pass" && (auth.spf === "pass" || auth.dkim === "pass");
}

function authSummary(auth: AuthSignals): string {
  return `SPF ${auth.spf}, DKIM ${auth.dkim}, DMARC ${auth.dmarc}`;
}
