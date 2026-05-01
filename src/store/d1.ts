import type { Contact, Entity, ProcessedEmail, RiskLevel } from "../pipeline/types";

interface EmailRow {
  id: string;
  received_at: string;
  sender_addr: string;
  sender_name: string | null;
  subject: string | null;
  recipients: string;
  cleaned_body: string | null;
  risk_level: RiskLevel;
  risk_reasons: string;
  labels: string | null;
  threat_flags: string | null;
}

const MAX_VECTOR_ERROR_LENGTH = 1000;

export interface EmailListFilters {
  limit?: number;
  offset?: number;
  riskFilter?: RiskLevel;
  since?: string;
  sender?: string;
}

export interface EmailSearchFilters {
  query: string;
  sender?: string;
  riskFilter?: RiskLevel;
  since?: string;
  limit?: number;
}

export async function storeProcessedEmail(db: D1Database, email: ProcessedEmail): Promise<void> {
  const statements = [
    db
      .prepare(
        `INSERT INTO emails (
          id, message_id, received_at, sender_addr, sender_name, subject, recipients, cleaned_body,
          has_attachments, risk_level, risk_reasons, labels, threat_flags, spf_result, dkim_result,
          dmarc_result, embedding_id
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      )
      .bind(
        email.id,
        email.messageId ?? null,
        email.receivedAt,
        email.sender.address,
        email.sender.name ?? null,
        email.subject,
        JSON.stringify(email.recipients),
        email.body,
        email.hasAttachments ? 1 : 0,
        email.riskLevel,
        JSON.stringify(email.riskReasons),
        JSON.stringify(email.labels),
        JSON.stringify(email.threatFlags),
        email.auth.spf,
        email.auth.dkim,
        email.auth.dmarc,
        email.embeddingId ?? null,
      ),
    ...email.entities.map((entity) => insertEntity(db, entity)),
  ];

  await db.batch(statements);
}

export async function setEmbeddingId(db: D1Database, emailId: string, embeddingId: string): Promise<void> {
  await db
    .prepare("UPDATE emails SET embedding_id = ?, vector_status = 'indexed', vector_error = NULL WHERE id = ?")
    .bind(embeddingId, emailId)
    .run();
}

export async function setVectorIndexFailed(db: D1Database, emailId: string, error: string): Promise<void> {
  await db
    .prepare("UPDATE emails SET vector_status = 'failed', vector_error = ? WHERE id = ?")
    .bind(error.slice(0, MAX_VECTOR_ERROR_LENGTH), emailId)
    .run();
}

export async function listEmails(db: D1Database, filters: EmailListFilters = {}) {
  const clauses: string[] = [];
  const bindings: unknown[] = [];

  if (filters.riskFilter) {
    clauses.push("risk_level = ?");
    bindings.push(filters.riskFilter);
  }

  if (filters.since) {
    clauses.push("received_at >= ?");
    bindings.push(filters.since);
  }

  if (filters.sender) {
    clauses.push("sender_addr = ?");
    bindings.push(filters.sender.toLowerCase());
  }

  const where = clauses.length > 0 ? `WHERE ${clauses.join(" AND ")}` : "";
  const limit = clamp(filters.limit ?? 25, 1, 100);
  const offset = Math.max(0, filters.offset ?? 0);

  const result = await db
    .prepare(
      `SELECT id, received_at, sender_addr, sender_name, subject, labels, risk_level
       FROM emails ${where}
       ORDER BY received_at DESC
       LIMIT ? OFFSET ?`,
    )
    .bind(...bindings, limit, offset)
    .all();

  return result.results.map(summaryFromRow);
}

export async function getEmail(db: D1Database, id: string) {
  const email = await db.prepare("SELECT * FROM emails WHERE id = ?").bind(id).first<EmailRow>();
  if (!email) return undefined;

  const entities = await db
    .prepare("SELECT entity_type, value, source_field FROM entities WHERE email_id = ? ORDER BY entity_type, value")
    .bind(id)
    .all();

  return gateEmail(email, entities.results);
}

export async function searchEmails(db: D1Database, filters: EmailSearchFilters) {
  const clauses = ["(subject LIKE ? OR cleaned_body LIKE ? OR sender_addr LIKE ?)"];
  const likeQuery = `%${filters.query}%`;
  const bindings: unknown[] = [likeQuery, likeQuery, likeQuery];

  if (filters.riskFilter) {
    clauses.push("risk_level = ?");
    bindings.push(filters.riskFilter);
  }

  if (filters.since) {
    clauses.push("received_at >= ?");
    bindings.push(filters.since);
  }

  if (filters.sender) {
    clauses.push("sender_addr = ?");
    bindings.push(filters.sender.toLowerCase());
  }

  const result = await db
    .prepare(
      `SELECT id, received_at, sender_addr, sender_name, subject, labels, risk_level
       FROM emails
       WHERE ${clauses.join(" AND ")}
       ORDER BY received_at DESC
       LIMIT ?`,
    )
    .bind(...bindings, clamp(filters.limit ?? 25, 1, 100))
    .all();

  return result.results.map(summaryFromRow);
}

export async function getEmailSummariesByIds(db: D1Database, ids: string[]) {
  if (ids.length === 0) return [];

  const placeholders = ids.map(() => "?").join(", ");
  const result = await db
    .prepare(
      `SELECT id, received_at, sender_addr, sender_name, subject, labels, risk_level
       FROM emails
       WHERE id IN (${placeholders})`,
    )
    .bind(...ids)
    .all();

  const byId = new Map(result.results.map((row) => [String(row.id), summaryFromRow(row)]));
  return ids.flatMap((id) => {
    const summary = byId.get(id);
    return summary ? [summary] : [];
  });
}

function insertEntity(db: D1Database, entity: Entity): D1PreparedStatement {
  return db
    .prepare("INSERT INTO entities (id, email_id, entity_type, value, source_field) VALUES (?, ?, ?, ?, ?)")
    .bind(entity.id, entity.emailId, entity.entityType, entity.value, entity.sourceField);
}

function summaryFromRow(row: Record<string, unknown>) {
  return {
    id: String(row.id),
    sender: {
      address: String(row.sender_addr),
      name: row.sender_name ? String(row.sender_name) : undefined,
    } satisfies Contact,
    subject: row.subject ? String(row.subject) : "",
    received_at: String(row.received_at),
    risk_level: row.risk_level as RiskLevel,
    labels: parseJsonArray(row.labels),
  };
}

function gateEmail(row: EmailRow, entities: unknown[]) {
  const output: Record<string, unknown> = {
    id: row.id,
    sender: {
      address: row.sender_addr,
      name: row.sender_name ?? undefined,
    },
    subject: row.subject ?? "",
    received_at: row.received_at,
    risk_level: row.risk_level,
    risk_reasons: parseJsonArray(row.risk_reasons),
    labels: parseJsonArray(row.labels),
  };

  if (row.risk_level === "yellow" || row.risk_level === "green") {
    output.recipients = parseJsonArray(row.recipients);
    output.entities = entities;
    output.threat_flags = parseJsonArray(row.threat_flags);
  }

  if (row.risk_level === "green") {
    output.cleaned_body = row.cleaned_body ?? "";
  }

  return output;
}

function parseJsonArray(value: unknown): unknown[] {
  if (typeof value !== "string") return [];

  try {
    const parsed = JSON.parse(value) as unknown;
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
