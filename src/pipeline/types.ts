export type RiskLevel = "red" | "yellow" | "green";
export type AuthResult = "pass" | "fail" | "softfail" | "neutral" | "none" | "unknown";

export interface Env {
  DB: D1Database;
  AI: Ai;
  VECTORS: VectorizeIndex;
  MCP_AGENT: DurableObjectNamespace;
  MCP_SHARED_SECRET?: string;
}

export interface Contact {
  name?: string;
  address: string;
}

export interface AuthSignals {
  spf: AuthResult;
  dkim: AuthResult;
  dmarc: AuthResult;
}

export interface ParsedInboundEmail {
  id: string;
  messageId?: string;
  receivedAt: string;
  sender: Contact;
  recipients: Contact[];
  subject: string;
  textBody: string;
  htmlBody: string;
  hasAttachments: boolean;
  auth: AuthSignals;
}

export interface CleanedEmail {
  id: string;
  messageId?: string;
  receivedAt: string;
  sender: Contact;
  recipients: Contact[];
  subject: string;
  body: string;
  hasAttachments: boolean;
  auth: AuthSignals;
}

export interface Classification {
  riskLevel: RiskLevel;
  riskReasons: string[];
  labels: string[];
}

export interface Entity {
  id: string;
  emailId: string;
  entityType: "email" | "url" | "date" | "name" | "phone";
  value: string;
  sourceField: "subject" | "body";
}

export interface ProcessedEmail extends CleanedEmail, Classification {
  threatFlags: string[];
  entities: Entity[];
  embeddingId?: string;
}

export interface EmailSummary {
  id: string;
  sender: Contact;
  subject: string;
  received_at: string;
  risk_level: RiskLevel;
  labels: string[];
}
