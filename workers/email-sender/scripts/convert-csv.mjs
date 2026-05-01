/**
 * Converts Phishing_validation_emails.csv → src/emailData.json
 * Run: node scripts/convert-csv.mjs
 */

import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const csvPath = path.join(__dirname, "..", "Phishing_validation_emails.csv");
const outPath = path.join(__dirname, "..", "src", "emailData.json");

const lines = fs.readFileSync(csvPath, "utf-8").trim().split("\n").slice(1); // skip header

const emails = lines.map((line) => {
  const match = line.match(/^(".*?"|[^,]+),(.+)$/);
  if (!match) return null;
  const text = match[1].replace(/^"|"$/g, "").trim();
  const type = match[2].trim();
  return { text, type };
}).filter(Boolean);

fs.writeFileSync(outPath, JSON.stringify(emails, null, 2));
console.log(`Converted ${emails.length} emails → src/emailData.json`);
