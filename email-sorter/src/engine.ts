import type {
  Category,
  Correction,
  Decision,
  MailSummary,
  PersistedState,
  Suggestion,
} from './types.js';
import { classifyByRule } from './classify/rules.js';
import { classifyBatch, LLM_BATCH_SIZE, QuotaExceededError, LlmError } from './classify/llm.js';
import { gate } from './classify/confidence.js';
import { NEEDS_REVIEW_ID } from './taxonomy.js';

/**
 * Orchestration of the three layers.
 *
 * The ordering is the whole economics of the tool: rules are free and instant,
 * so they run first and absorb the repetitive majority; the LLM only sees mail
 * from senders we've never resolved. Over time layer 1 grows and layer 2 shrinks
 * toward nothing, which is what keeps this inside a free tier indefinitely.
 */

export interface SortOutcome {
  decisions: Decision[];
  /** How many LLM requests were spent. Surfaced in the UI as quota feedback. */
  llmRequests: number;
  /** How many messages layer 1 resolved for free. */
  resolvedByRule: number;
  /** True when quota ran out mid-run and the remainder fell back to rules only. */
  degraded: boolean;
  /** User-facing explanation when something went sideways. */
  note?: string;
}

export async function classifyAll(
  mail: MailSummary[],
  state: PersistedState,
  corpus: Correction[],
): Promise<SortOutcome> {
  const { settings, taxonomy, senderRules } = state;
  const decisions: Decision[] = [];
  const needsLlm: MailSummary[] = [];

  // --- Layer 1 -------------------------------------------------------------
  for (const item of mail) {
    const hit = classifyByRule(senderRules, item);
    if (hit) {
      const decision = gate(item.id, [hit], settings.confidenceThreshold);
      decisions.push({ ...decision, source: 'rule' });
    } else {
      needsLlm.push(item);
    }
  }

  const resolvedByRule = decisions.length;

  // Rules-only by configuration: everything unknown stays unknown rather than
  // any content leaving the mailbox.
  if (settings.dataSharing === 'rules') {
    for (const item of needsLlm) decisions.push(unresolved(item.id, 'Sender not recognized yet.'));
    return {
      decisions,
      llmRequests: 0,
      resolvedByRule,
      degraded: false,
      note:
        needsLlm.length > 0
          ? `${needsLlm.length} message(s) need review: sharing is set to rules-only, so no content was sent anywhere.`
          : undefined,
    };
  }

  if (!settings.geminiApiKey && needsLlm.length > 0) {
    for (const item of needsLlm) decisions.push(unresolved(item.id, 'No API key configured.'));
    return {
      decisions,
      llmRequests: 0,
      resolvedByRule,
      degraded: false,
      note: 'Add a Gemini API key in Settings to classify mail from unfamiliar senders.',
    };
  }

  // --- Layer 2 -------------------------------------------------------------
  let llmRequests = 0;
  let degraded = false;
  let note: string | undefined;

  for (let i = 0; i < needsLlm.length; i += LLM_BATCH_SIZE) {
    const batch = needsLlm.slice(i, i + LLM_BATCH_SIZE);

    if (degraded) {
      for (const item of batch) decisions.push(unresolved(item.id, 'Daily AI quota reached.'));
      continue;
    }

    try {
      const results = await classifyBatch(batch, taxonomy, corpus, settings);
      llmRequests++;
      for (const item of batch) {
        const ranked = results.get(item.id);
        decisions.push(
          ranked && ranked.length > 0
            ? gate(item.id, ranked, settings.confidenceThreshold)
            : unresolved(item.id, 'The model returned no usable category.'),
        );
      }
    } catch (err) {
      if (err instanceof QuotaExceededError) {
        // Not a failure. The whole point of layer 1 is that running out of
        // free quota degrades gracefully instead of breaking the tool.
        degraded = true;
        note = err.message;
        for (const item of batch) decisions.push(unresolved(item.id, 'Daily AI quota reached.'));
        continue;
      }
      if (err instanceof LlmError) {
        note = err.message;
        for (const item of batch) decisions.push(unresolved(item.id, 'Classification failed.'));
        continue;
      }
      throw err;
    }
  }

  return { decisions, llmRequests, resolvedByRule, degraded, note };
}

function unresolved(messageId: string, reason: string): Decision {
  return {
    messageId,
    ranked: [{ categoryId: NEEDS_REVIEW_ID, confidence: 0, reason }],
    source: 'unresolved',
    gated: true,
  };
}

/**
 * Find corrections the user made in Outlook itself.
 *
 * She changes a category the normal way, we notice on the next pass. This is the
 * feedback path that matters, because it costs her nothing to use - there is no
 * widget to discover and no habit to form. It works by comparing our provenance
 * stamp against the categories actually on the message.
 *
 * A removed category is not treated as a correction: it says "not this" without
 * saying "but that", and inventing a target from silence would poison the rules.
 */
export function detectCorrections(mail: MailSummary[], taxonomy: Category[]): Correction[] {
  const nameToId = new Map(taxonomy.map((c) => [c.name.trim().toLowerCase(), c.id] as const));
  const corrections: Correction[] = [];

  for (const item of mail) {
    if (!item.assigned) continue;

    const presentIds = item.categories
      .map((name) => nameToId.get(name.trim().toLowerCase()))
      .filter((id): id is string => Boolean(id));

    const realIds = presentIds.filter((id) => id !== NEEDS_REVIEW_ID);
    if (realIds.length === 0) continue;

    // Still carrying what we assigned - no disagreement.
    if (realIds.includes(item.assigned.categoryId)) continue;

    // Exactly one replacement is an unambiguous correction. Several of our
    // categories at once means she's using them as multi-labels, which is a
    // legitimate choice but not a signal we can learn a single mapping from.
    if (realIds.length !== 1) continue;

    corrections.push({
      sender: item.from,
      subject: item.subject,
      fromCategoryId: item.assigned.categoryId,
      toCategoryId: realIds[0] as string,
      at: new Date().toISOString(),
    });
  }

  return corrections;
}

/** Messages worth spending a classification on: unlabelled, or previously gated. */
export function needsClassification(mail: MailSummary[], taxonomy: Category[]): MailSummary[] {
  const ourNames = new Set(taxonomy.map((c) => c.name.trim().toLowerCase()));
  const reviewName = taxonomy
    .find((c) => c.id === NEEDS_REVIEW_ID)
    ?.name.trim()
    .toLowerCase();

  return mail.filter((item) => {
    const ours = item.categories.filter((n) => ourNames.has(n.trim().toLowerCase()));
    if (ours.length === 0) return true;
    // Retry anything parked in Needs Review: rules learned since the last run,
    // or newly added few-shot examples, may resolve it now.
    return ours.every((n) => n.trim().toLowerCase() === reviewName);
  });
}

/** Convenience for the UI: the top suggestion, or null when unresolved. */
export function topSuggestion(decision: Decision): Suggestion | null {
  const first = decision.ranked[0];
  if (!first || first.categoryId === NEEDS_REVIEW_ID) return null;
  return first;
}
