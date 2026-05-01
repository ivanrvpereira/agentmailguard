import PostalMime, { type Address, type Email, type Mailbox } from "postal-mime";
import { setEmbeddingId, setVectorIndexFailed, storeProcessedEmail } from "../store/d1";
import { embedAndStoreEmail } from "../store/vectorize";
import { authSignalsFromHeaders } from "./auth";
import { cleanEmail } from "./cleaner";
import { classifyEmail } from "./classifier";
import { detectThreats } from "./detector";
import { extractEntities } from "./extractor";
import type { Contact, Env, ParsedInboundEmail, ProcessedEmail } from "./types";

export async function processInboundEmail(message: ForwardableEmailMessage, env: Env): Promise<void> {
  const startedAt = Date.now();
  logEmailEvent("email_received", {
    size: message.rawSize,
  });

  try {
    const parsed = await PostalMime.parse(message.raw, { attachmentEncoding: "base64" });
    const inbound = toParsedInboundEmail(message, parsed);
    const cleaned = cleanEmail(inbound);
    const threatFlags = detectThreats(cleaned);
    const classification = await classifyEmail(env.AI, cleaned, threatFlags);
    const entities = extractEntities(cleaned);

    const processed: ProcessedEmail = {
      ...cleaned,
      ...classification,
      threatFlags,
      entities,
    };

    await storeProcessedEmail(env.DB, processed);
    logEmailEvent("email_processed", {
      id: processed.id,
      risk: processed.riskLevel,
      spf: processed.auth.spf,
      dkim: processed.auth.dkim,
      dmarc: processed.auth.dmarc,
      threats: processed.threatFlags.length,
      flags: processed.threatFlags,
      entities: processed.entities.length,
      attachments: processed.hasAttachments,
      duration_ms: Date.now() - startedAt,
    });

    try {
      const embeddingId = await embedAndStoreEmail(
        env.AI,
        env.VECTORS,
        processed.id,
        `${processed.subject}\n${processed.body}`,
      );

      if (embeddingId) {
        await setEmbeddingId(env.DB, processed.id, embeddingId);
        logEmailEvent("email_vector_indexed", {
          id: processed.id,
          duration_ms: Date.now() - startedAt,
        });
      }
    } catch (error) {
      await setVectorIndexFailed(env.DB, processed.id, errorMessage(error));
      logEmailEvent("email_vector_failed", {
        id: processed.id,
        error_type: errorType(error),
        duration_ms: Date.now() - startedAt,
      });
    }
  } catch (error) {
    logEmailEvent("email_processing_failed", {
      error_type: errorType(error),
      duration_ms: Date.now() - startedAt,
    });
    throw error;
  }
}

function toParsedInboundEmail(message: ForwardableEmailMessage, parsed: Email): ParsedInboundEmail {
  return {
    id: crypto.randomUUID(),
    messageId: parsed.messageId,
    receivedAt: new Date().toISOString(),
    sender: contactFromAddress(parsed.from) ?? contactFromEnvelope(message.from),
    recipients: contactsFromParsedEmail(parsed, message.to),
    subject: parsed.subject ?? "",
    textBody: parsed.text ?? "",
    htmlBody: parsed.html ?? "",
    hasAttachments: parsed.attachments.length > 0,
    auth: authSignalsFromHeaders(parsed.headers),
  };
}

function contactsFromParsedEmail(parsed: Email, envelopeTo: string): Contact[] {
  const contacts = [...contactsFromAddresses(parsed.to), ...contactsFromAddresses(parsed.cc), ...contactsFromAddresses(parsed.bcc)];
  return contacts.length > 0 ? contacts : [contactFromEnvelope(envelopeTo)];
}

function contactFromAddress(address?: Address): Contact | undefined {
  if (!address) return undefined;

  if (address.address) {
    return {
      address: address.address,
      name: address.name || undefined,
    };
  }

  return Array.isArray(address.group) ? mailboxToContact(address.group[0]) : undefined;
}

function contactsFromAddresses(addresses?: Address[]): Contact[] {
  return addresses?.flatMap((address) => {
    const contact = contactFromAddress(address);
    return contact ? [contact] : [];
  }) ?? [];
}

function mailboxToContact(mailbox?: Mailbox): Contact | undefined {
  if (!mailbox?.address) return undefined;
  return {
    address: mailbox.address,
    name: mailbox.name || undefined,
  };
}

function contactFromEnvelope(address: string): Contact {
  return { address };
}

function logEmailEvent(event: string, fields: Record<string, unknown>): void {
  const parts = [`event=${event}`];

  for (const [key, value] of Object.entries(fields)) {
    if (value === undefined) continue;
    parts.push(`${key}=${formatLogValue(value)}`);
  }

  console.log(parts.join(" "));
}

function formatLogValue(value: unknown): string {
  if (value === null) return "-";

  if (Array.isArray(value)) {
    return value.length > 0 ? value.map(formatListValue).join(",") : "-";
  }

  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    return entries.length > 0 ? entries.map(([key, entryValue]) => `${key}:${formatListValue(entryValue)}`).join(",") : "-";
  }

  return formatListValue(value);
}

function formatListValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "-";
  const text = String(value).replace(/\s+/g, "_");
  return text.length > 0 ? text : "-";
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function errorType(error: unknown): string {
  return error instanceof Error ? error.name : typeof error;
}
