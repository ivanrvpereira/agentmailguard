import type { CleanedEmail } from "./types";

const HOMOGLYPHS: Record<string, string> = {
  а: "a",
  е: "e",
  о: "o",
  р: "p",
  с: "c",
  х: "x",
  у: "y",
  А: "A",
  В: "B",
  Е: "E",
  К: "K",
  М: "M",
  Н: "H",
  О: "O",
  Р: "P",
  С: "C",
  Т: "T",
  Х: "X",
};

const INJECTION_PATTERNS: Array<[string, RegExp]> = [
  ["instruction_override", /\b(ignore|disregard|forget)\b.{0,80}\b(previous|prior|above|system|developer)\b.{0,40}\b(instructions?|message|prompt)\b/i],
  ["system_prompt_marker", /\b(system|developer|assistant|user)\s*:/i],
  ["authority_claim", /\b(you are now|act as|from now on|new instructions?)\b/i],
  ["fake_conversation", /\b(begin|start)\s+(conversation|transcript)|###\s*(system|developer|assistant|user)/i],
  ["roleplay_attack", /\bpretend\b.{0,80}\b(no restrictions|not bound|bypass|jailbreak)\b/i],
];

export function detectThreats(email: CleanedEmail): string[] {
  const text = normalizeForDetection(`${email.subject}\n${email.body}`);
  const flags = new Set<string>();

  for (const [flag, pattern] of INJECTION_PATTERNS) {
    if (pattern.test(text)) flags.add(flag);
  }

  if (/https?:\/\//i.test(text)) flags.add("residual_url");
  if (/data:[\w/+.-]+;base64,|\[removed:data-uri\]/i.test(text)) flags.add("data_uri");
  if (/\b[A-Za-z0-9+/]{80,}={0,2}\b|\[removed:base64-blob\]/.test(text)) flags.add("base64_blob");
  if (/\b[a-f0-9]{80,}\b|\[removed:hex-blob\]/i.test(text)) flags.add("hex_payload");

  return [...flags];
}

function normalizeForDetection(value: string): string {
  return stripZalgo(collapseSpacedOutText(mapHomoglyphs(value)));
}

function mapHomoglyphs(value: string): string {
  return [...value].map((char) => HOMOGLYPHS[char] ?? char).join("");
}

function collapseSpacedOutText(value: string): string {
  return value.replace(/\b(?:[a-zA-Z]\s+){4,}[a-zA-Z]\b/g, (match) => match.replace(/\s+/g, ""));
}

function stripZalgo(value: string): string {
  return value.normalize("NFD").replace(/[\u0300-\u036f]/g, "").normalize("NFC");
}
