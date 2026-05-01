import type { Ai } from "@cloudflare/workers-types";

interface EmbeddingResponse {
  data?: number[][];
}

export async function embedAndStoreEmail(
  ai: Ai,
  vectors: VectorizeIndex,
  emailId: string,
  text: string,
): Promise<string | undefined> {
  const vector = await embedText(ai, text);
  if (!vector) return undefined;

  const embeddingId = `email:${emailId}`;
  await vectors.upsert([
    {
      id: embeddingId,
      values: vector,
      metadata: { email_id: emailId },
    },
  ]);

  return embeddingId;
}

export async function semanticSearchIds(ai: Ai, vectors: VectorizeIndex, query: string, limit = 10): Promise<string[]> {
  const vector = await embedText(ai, query);
  if (!vector) return [];

  const matches = await vectors.query(vector, {
    topK: Math.min(Math.max(limit, 1), 50),
    returnMetadata: true,
  });

  return matches.matches.flatMap((match) => {
    const metadata = match.metadata as Record<string, unknown> | undefined;
    const emailId = metadata?.email_id;
    return typeof emailId === "string" ? [emailId] : [];
  });
}

async function embedText(ai: Ai, text: string): Promise<number[] | undefined> {
  const response = (await ai.run("@cf/baai/bge-base-en-v1.5", {
    text: [text.slice(0, 8000)],
  })) as EmbeddingResponse;

  return response.data?.[0];
}
