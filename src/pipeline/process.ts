import PostalMime, { type Address, type Email, type Mailbox } from "postal-mime";
import { storeProcessedEmail, setEmbeddingId } from "../store/d1";
import { embedAndStoreEmail } from "../store/vectorize";
import { cleanEmail } from "./cleaner";
import { classifyEmail } from "./classifier";
import { detectThreats } from "./detector";
import { extractEntities } from "./extractor";
import type { AuthResult, AuthSignals, Contact, Env, ParsedInboundEmail, ProcessedEmail } from "./types";

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
    console.warn("Vectorize embedding failed", error);
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
    auth: authSignalsFromHeaders(message.headers),
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

function authSignalsFromHeaders(headers: Headers): AuthSignals {
  const authResults = headers.get("Authentication-Results") ?? "";

  return {
    spf: authResult(authResults, "spf"),
    dkim: authResult(authResults, "dkim"),
    dmarc: authResult(authResults, "dmarc"),
  };
}

function authResult(value: string, key: "spf" | "dkim" | "dmarc"): AuthResult {
  const match = value.toLowerCase().match(new RegExp(`\\b${key}=([a-z]+)`));
  const result = match?.[1];

  if (
    result === "pass" ||
    result === "fail" ||
    result === "softfail" ||
    result === "neutral" ||
    result === "none"
  ) {
    return result;
  }

  return "unknown";
}
