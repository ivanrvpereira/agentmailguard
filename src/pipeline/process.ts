import PostalMime, { type Address, type Email, type Mailbox } from "postal-mime";
import { setEmbeddingId, setVectorIndexFailed, storeProcessedEmail } from "../store/d1";
import { embedAndStoreEmail } from "../store/vectorize";
import { cleanEmail } from "./cleaner";
import { classifyEmail } from "./classifier";
import { detectThreats } from "./detector";
import { extractEntities } from "./extractor";
import type { AuthSignals, Contact, Env, ParsedInboundEmail, ProcessedEmail } from "./types";

export async function processInboundEmail(message: ForwardableEmailMessage, env: Env): Promise<void> {
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

  try {
    const embeddingId = await embedAndStoreEmail(
      env.AI,
      env.VECTORS,
      processed.id,
      `${processed.subject}\n${processed.body}`,
    );

    if (embeddingId) {
      await setEmbeddingId(env.DB, processed.id, embeddingId);
    }
  } catch (error) {
    await setVectorIndexFailed(env.DB, processed.id, errorMessage(error));
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
    auth: unknownAuthSignals(),
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

function unknownAuthSignals(): AuthSignals {
  // Inbound Authentication-Results headers are email-derived and not provenance-verified here.
  return {
    spf: "unknown",
    dkim: "unknown",
    dmarc: "unknown",
  };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
