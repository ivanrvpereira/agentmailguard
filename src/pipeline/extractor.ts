import type { CleanedEmail, Entity } from "./types";

type SourceField = Entity["sourceField"];
type EntityType = Entity["entityType"];

const PATTERNS: Array<[EntityType, RegExp]> = [
  ["email", /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi],
  ["url", /\bhttps?:\/\/[^\s<>()]+/gi],
  ["phone", /\b(?:\+?\d[\d .()-]{7,}\d)\b/g],
  ["date", /\b\d{4}-\d{2}-\d{2}\b|\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+\d{1,2},?\s+\d{4}\b/gi],
];

export function extractEntities(email: CleanedEmail): Entity[] {
  const entities: Entity[] = [];
  const seen = new Set<string>();

  collectFromField(email.id, "subject", email.subject, entities, seen);
  collectFromField(email.id, "body", email.body, entities, seen);

  return entities.slice(0, 100);
}

function collectFromField(
  emailId: string,
  sourceField: SourceField,
  value: string,
  entities: Entity[],
  seen: Set<string>,
): void {
  for (const [entityType, pattern] of PATTERNS) {
    for (const match of value.matchAll(pattern)) {
      const entityValue = match[0].replace(/[.,;:!?]+$/, "");
      const key = `${entityType}:${sourceField}:${entityValue.toLowerCase()}`;
      if (seen.has(key)) continue;

      seen.add(key);
      entities.push({
        id: crypto.randomUUID(),
        emailId,
        entityType,
        value: entityValue,
        sourceField,
      });
    }
  }
}
