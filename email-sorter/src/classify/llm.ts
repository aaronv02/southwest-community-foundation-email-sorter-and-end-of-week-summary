import type { Category, Correction, MailSummary, Settings, Suggestion } from '../types.js';
import { NEEDS_REVIEW_ID, selectableCategories } from '../taxonomy.js';

/**
 * Layer 2: the LLM, for senders no rule covers.
 *
 * Called against Gemini's native REST endpoint
 * (`v1beta/models/{model}:generateContent`), which returns CORS headers and so
 * works directly from the taskpane with no backend. Deliberately NOT the
 * OpenAI-compatibility shim at `/v1beta/openai/`, which has reported CORS
 * preflight failures from browsers.
 *
 * Quota is the binding constraint, not latency, so messages are classified in
 * batches of ~20 per request. Google no longer publishes a stable per-model
 * free-tier table, so nothing here hardcodes a limit: we read what the API tells
 * us and degrade to rules-only on 429 rather than pretending to know the cap.
 */

const ENDPOINT_BASE = 'https://generativelanguage.googleapis.com/v1beta/models';

export const LLM_BATCH_SIZE = 20;

export class QuotaExceededError extends Error {
  constructor(
    message: string,
    /** Seconds to wait, when the API supplies it. */
    readonly retryAfter?: number,
  ) {
    super(message);
  }
}

export class LlmError extends Error {}

/** Gemini's structured-output schema, so we never parse prose. */
const RESPONSE_SCHEMA = {
  type: 'OBJECT',
  properties: {
    results: {
      type: 'ARRAY',
      items: {
        type: 'OBJECT',
        properties: {
          ref: { type: 'INTEGER' },
          ranked: {
            type: 'ARRAY',
            items: {
              type: 'OBJECT',
              properties: {
                category: { type: 'STRING' },
                confidence: { type: 'NUMBER' },
                reason: { type: 'STRING' },
              },
              required: ['category', 'confidence', 'reason'],
            },
          },
        },
        required: ['ref', 'ranked'],
      },
    },
  },
  required: ['results'],
} as const;

const SYSTEM_INSTRUCTION = `You sort a nonprofit executive director's email into categories.

Rules:
- Choose only from the category IDs given. Never invent one.
- Return the three most plausible categories per message, best first, each with a calibrated confidence between 0 and 1.
- Confidence must be honest. If a message could credibly belong to two categories, say 0.5, not 0.9. Under-confidence is cheap; over-confidence causes mislabelled mail.
- Never choose "${NEEDS_REVIEW_ID}". Low confidence across the board is how you express uncertainty.
- Judge by purpose, not vocabulary. A message mentioning a grant is not necessarily about grantmaking.
- Automated and bulk mail is usually easy: statements and payout reports are financial records, mailing lists with unsubscribe links are sector reading.
- Keep each reason under 12 words and concrete, citing what in the message decided it.`;

interface GeminiResponse {
  candidates?: {
    content?: { parts?: { text?: string }[] };
    finishReason?: string;
  }[];
  promptFeedback?: { blockReason?: string };
}

interface ParsedResults {
  results: { ref: number; ranked: { category: string; confidence: number; reason: string }[] }[];
}

function renderTaxonomy(taxonomy: Category[]): string {
  return selectableCategories(taxonomy)
    .map((c) => `- ${c.id} ("${c.name}"): ${c.description}`)
    .join('\n');
}

/**
 * Past corrections as few-shot examples.
 *
 * This is the entire learning mechanism for mail from unfamiliar senders: no
 * retraining, no embeddings, just the user's own past judgements carried into
 * the prompt. Most recent first, since her taxonomy drifts over time and recent
 * decisions reflect what she means now.
 */
function renderExamples(corrections: Correction[], limit: number): string {
  const usable = corrections.filter((c) => c.toCategoryId !== NEEDS_REVIEW_ID).slice(0, limit);
  if (usable.length === 0) return '';
  const lines = usable.map(
    (c) => `- From ${c.sender}, subject "${truncate(c.subject, 80)}" -> ${c.toCategoryId}`,
  );
  return `\nThe user has previously corrected these, so treat them as authoritative:\n${lines.join('\n')}\n`;
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : `${s.slice(0, n - 1)}…`;
}

function renderMessages(batch: MailSummary[], includePreview: boolean): string {
  return batch
    .map((m, i) => {
      const parts = [
        `[${i}]`,
        `from: ${m.fromName ? `${m.fromName} <${m.from}>` : m.from}`,
        `subject: ${truncate(m.subject, 200)}`,
      ];
      if (m.hasAttachments) parts.push('has attachments: yes');
      if (m.listId) parts.push('mailing list: yes');
      if (includePreview && m.preview) {
        parts.push(`preview: ${truncate(m.preview.replace(/\s+/g, ' '), 500)}`);
      }
      return parts.join('\n');
    })
    .join('\n\n');
}

export function buildPrompt(
  batch: MailSummary[],
  taxonomy: Category[],
  corrections: Correction[],
  includePreview: boolean,
): string {
  return `Categories:
${renderTaxonomy(taxonomy)}
${renderExamples(corrections, 15)}
Classify each message below. Return one result per message, using its bracketed number as "ref".

${renderMessages(batch, includePreview)}`;
}

/**
 * Classify one batch. Returns a map from message id to ranked suggestions.
 *
 * Any category id the model returns that isn't in the taxonomy is dropped
 * rather than trusted - structured output constrains shape, not vocabulary.
 */
export async function classifyBatch(
  batch: MailSummary[],
  taxonomy: Category[],
  corrections: Correction[],
  settings: Settings,
): Promise<Map<string, Suggestion[]>> {
  if (!settings.geminiApiKey) throw new LlmError('No Gemini API key is configured.');
  if (settings.dataSharing === 'rules') {
    throw new LlmError('Data sharing is set to rules-only; the LLM must not be called.');
  }

  const includePreview = settings.dataSharing === 'full';
  const prompt = buildPrompt(batch, taxonomy, corrections, includePreview);

  const res = await fetch(`${ENDPOINT_BASE}/${settings.model}:generateContent`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      // Header rather than ?key= so the key never lands in a URL, where it
      // would be liable to end up in logs or history.
      'x-goog-api-key': settings.geminiApiKey,
    },
    body: JSON.stringify({
      systemInstruction: { parts: [{ text: SYSTEM_INSTRUCTION }] },
      contents: [{ role: 'user', parts: [{ text: prompt }] }],
      generationConfig: {
        // Classification, not composition: determinism is what we want.
        temperature: 0,
        responseMimeType: 'application/json',
        responseSchema: RESPONSE_SCHEMA,
      },
    }),
  });

  if (res.status === 429) {
    const retry = Number(res.headers.get('Retry-After') ?? '');
    throw new QuotaExceededError(
      'Gemini free-tier quota reached. Falling back to sender rules only.',
      Number.isFinite(retry) ? retry : undefined,
    );
  }
  if (!res.ok) {
    const detail = await res.text().catch(() => '');
    throw new LlmError(`Gemini request failed (${res.status}). ${truncate(detail, 300)}`);
  }

  const body = (await res.json()) as GeminiResponse;
  if (body.promptFeedback?.blockReason) {
    throw new LlmError(`Gemini blocked the request: ${body.promptFeedback.blockReason}`);
  }

  const text = body.candidates?.[0]?.content?.parts?.[0]?.text;
  if (!text) throw new LlmError('Gemini returned no content.');

  let parsed: ParsedResults;
  try {
    parsed = JSON.parse(text) as ParsedResults;
  } catch {
    throw new LlmError('Gemini returned unparseable JSON despite a response schema.');
  }

  const validIds = new Set(selectableCategories(taxonomy).map((c) => c.id));
  const out = new Map<string, Suggestion[]>();

  for (const result of parsed.results ?? []) {
    const mail = batch[result.ref];
    if (!mail) continue;

    const ranked = (result.ranked ?? [])
      .filter((r) => validIds.has(r.category))
      .map((r) => ({
        categoryId: r.category,
        confidence: clamp01(r.confidence),
        reason: r.reason?.trim() || 'Classified by content.',
      }))
      .sort((a, b) => b.confidence - a.confidence)
      .slice(0, 3);

    if (ranked.length > 0) out.set(mail.id, ranked);
  }

  return out;
}

function clamp01(n: number): number {
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(1, n));
}
