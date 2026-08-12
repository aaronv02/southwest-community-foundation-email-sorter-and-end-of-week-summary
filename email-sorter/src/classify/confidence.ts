import type { Decision, Settings, Suggestion } from '../types.js';
import { NEEDS_REVIEW_ID } from '../taxonomy.js';

/**
 * Layer 3: the honesty gate.
 *
 * A confident wrong label is worse than an admitted unknown - it teaches the
 * user the tool can't be trusted, and they stop looking at the labels at all.
 * So anything that doesn't clear the threshold becomes Needs Review.
 */

/** Number of alternatives surfaced in the UI. */
export const RANKED_LIMIT = 3;

/**
 * Turn raw suggestions into a decision.
 *
 * Ranked alternatives are preserved even when gated, because the review UI shows
 * them as one-click choices. Offering three options is what lifts practical
 * accuracy well above a single forced guess in personal-email foldering.
 */
export function gate(messageId: string, ranked: Suggestion[], threshold: number): Decision {
  const cleaned = ranked
    .filter((s) => s.categoryId !== NEEDS_REVIEW_ID)
    .sort((a, b) => b.confidence - a.confidence)
    .slice(0, RANKED_LIMIT);

  if (cleaned.length === 0) {
    return {
      messageId,
      ranked: [
        { categoryId: NEEDS_REVIEW_ID, confidence: 0, reason: 'No category could be determined.' },
      ],
      source: 'unresolved',
      gated: true,
    };
  }

  const top = cleaned[0] as Suggestion;
  const runnerUp = cleaned[1];

  // Two separate ways to be unsure, and both matter. An absolute low score means
  // nothing fits; a narrow margin between the top two means something fits but
  // we can't tell which. Either one should defer to the user.
  const belowThreshold = top.confidence < threshold;
  const tooClose = runnerUp !== undefined && top.confidence - runnerUp.confidence < 0.1;

  return {
    messageId,
    ranked: cleaned,
    source: 'llm',
    gated: belowThreshold || tooClose,
  };
}

/** The category to actually write, honouring the gate. */
export function categoryToApply(decision: Decision): string {
  if (decision.gated) return NEEDS_REVIEW_ID;
  return (decision.ranked[0] as Suggestion).categoryId;
}

/**
 * Whether to write labels without asking.
 *
 * 'graduated' is the interesting mode: it stays in suggest-and-approve until the
 * tool has earned it, then labels on its own. The bar is deliberately high -
 * going auto too early is exactly how a sorting tool loses a user's trust.
 */
export function shouldAutoApply(settings: Settings): boolean {
  switch (settings.autonomy) {
    case 'auto':
      return true;
    case 'suggest':
      return false;
    case 'graduated':
      return settings.agreementRate >= 0.85;
  }
}

/**
 * Update the rolling agreement rate.
 *
 * Exponentially weighted so recent behaviour dominates: if the taxonomy changes
 * and accuracy drops, 'graduated' mode should fall back to asking rather than
 * coasting on a good history.
 */
export function updateAgreement(current: number, agreed: boolean, weight = 0.1): number {
  const observation = agreed ? 1 : 0;
  return Number((current * (1 - weight) + observation * weight).toFixed(4));
}
