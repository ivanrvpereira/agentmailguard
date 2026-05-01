import type { CleanedEmail, Contact, ParsedInboundEmail } from "./types";

const MAX_ENTITY_DECODE_ITERATIONS = 10;
const SUBJECT_LIMIT = 500;
const BODY_LIMIT = 8000;

const INVISIBLE_CHARS = /[\u200B-\u200F\u202A-\u202E\u2060-\u206F\uFE00-\uFE0F\u{E0000}-\u{E007F}]/gu;
const CONTROL_CHARS = /[\u0000-\u001F\u007F]/g;

export function cleanEmail(email: ParsedInboundEmail): CleanedEmail {
  const bodySource = [email.textBody, stripHtml(email.htmlBody)].filter(Boolean).join("\n");

  return {
    ...email,
    sender: cleanContact(email.sender),
    recipients: email.recipients.map(cleanContact),
    subject: truncate(cleanText(email.subject), SUBJECT_LIMIT),
    body: truncate(cleanText(bodySource), BODY_LIMIT),
  };
}

export function cleanText(value: string): string {
  return collapseWhitespace(
    neutralizePayloads(
      removeInvisibleCharacters(decodeEntities(value).normalize("NFC")),
    ),
  ).trim();
}

export function stripHtml(value: string): string {
  const withoutIgnoredContent = value
    .replace(/<!--[\s\S]*?-->/g, " ")
    .replace(/<script\b[\s\S]*?<\/script>/gi, " ")
    .replace(/<style\b[\s\S]*?<\/style>/gi, " ");
  const linkTargets = extractHtmlLinkTargets(withoutIgnoredContent);
  const text = withoutIgnoredContent.replace(/<[^>]+>/g, " ");

  return [text, ...linkTargets].join("\n");
}

function extractHtmlLinkTargets(value: string): string[] {
  const targets = new Set<string>();
  const attributePattern = /\s(?:href|src|action)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))/gi;

  for (const match of value.matchAll(attributePattern)) {
    const target = match[1] ?? match[2] ?? match[3] ?? "";
    if (/^(?:https?:|data:)/i.test(target)) targets.add(target);
  }

  return [...targets];
}

function cleanContact(contact: Contact): Contact {
  return {
    address: cleanDisplayField(contact.address).toLowerCase(),
    name: contact.name ? cleanDisplayField(contact.name) : undefined,
  };
}

function cleanDisplayField(value: string): string {
  return removeInvisibleCharacters(decodeEntities(value).normalize("NFC"))
    .replace(CONTROL_CHARS, "")
    .trim();
}

function decodeEntities(value: string): string {
  let decoded = value;

  for (let i = 0; i < MAX_ENTITY_DECODE_ITERATIONS; i += 1) {
    const next = decoded.replace(/&(#x?[0-9a-f]+|[a-z]+);/gi, (_, entity: string) => {
      const lower = entity.toLowerCase();

      if (lower.startsWith("#x")) {
        return codePointToString(Number.parseInt(lower.slice(2), 16));
      }

      if (lower.startsWith("#")) {
        return codePointToString(Number.parseInt(lower.slice(1), 10));
      }

      return namedEntity(lower);
    });

    if (next === decoded) break;
    decoded = next;
  }

  return decoded;
}

function codePointToString(codePoint: number): string {
  if (!Number.isFinite(codePoint)) return "";

  try {
    return String.fromCodePoint(codePoint);
  } catch {
    return "";
  }
}

function namedEntity(entity: string): string {
  const entities: Record<string, string> = {
    amp: "&",
    apos: "'",
    gt: ">",
    lt: "<",
    nbsp: " ",
    quot: '"',
  };

  return entities[entity] ?? "";
}

function removeInvisibleCharacters(value: string): string {
  return value.replace(INVISIBLE_CHARS, "");
}

function neutralizePayloads(value: string): string {
  return value
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "[removed:markdown-image]")
    .replace(/\[[^\]]+\]\((?:https?:|data:)[^)]*\)/gi, "[removed:markdown-link]")
    .replace(/data:[\w/+.-]+;base64,[a-z0-9+/=]+/gi, "[removed:data-uri]")
    .replace(/```[\s\S]*?```/g, "[removed:code-fence]")
    .replace(/\b(?:[A-Za-z0-9+/]{80,}={0,2})\b/g, "[removed:base64-blob]")
    .replace(/\b(?:[a-f0-9]{80,})\b/gi, "[removed:hex-blob]");
}

function collapseWhitespace(value: string): string {
  return value.replace(/\r\n?/g, "\n").replace(/[\t ]+/g, " ").replace(/\n{3,}/g, "\n\n");
}

function truncate(value: string, limit: number): string {
  return value.length > limit ? value.slice(0, limit) : value;
}
