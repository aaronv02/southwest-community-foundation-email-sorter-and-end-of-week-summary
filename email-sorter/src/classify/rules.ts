import type { Category, Correction, MailSummary, SenderRule, Suggestion } from '../types.js';
import { NEEDS_REVIEW_ID } from '../taxonomy.js';

/**
 * Layer 1: learned sender rules.
 *
 * Free, instant, deterministic, and by far the highest-precision signal in
 * personal email - who sent it predicts where it belongs better than anything in
 * the body. This layer exists to keep the LLM's scarce free-tier quota for mail
 * that actually needs judgement, and it doubles as the pool of candidates for
 * promotion into native Outlook rules.
 */

export function normalizeSender(address: string): string {
  return address.trim().toLowerCase();
}

/** "@example.org" for "person@example.org". Empty string if unparseable. */
export function domainOf(address: string): string {
  const at = address.lastIndexOf('@');
  return at === -1 ? '' : address.slice(at).toLowerCase();
}

/**
 * Best matching rule for a sender.
 *
 * An exact address always beats a domain rule. That ordering matters at a
 * foundation: mail from `@coloradogives.org` is Finance in general, but a named
 * relationship manager there might be filed differently, and the specific rule
 * must win.
 */
export function matchSender(rules: SenderRule[], from: string): SenderRule | undefined {
  const sender = normalizeSender(from);
  if (!sender) return undefined;

  const exact = rules.find((r) => r.pattern === sender);
  if (exact) return exact;

  const domain = domainOf(sender);
  if (!domain) return undefined;
  return rules.find((r) => r.pattern === domain);
}

/** Rule-layer verdict, or null when this sender is unknown. */
export function classifyByRule(rules: SenderRule[], mail: MailSummary): Suggestion | null {
  const rule = matchSender(rules, mail.from);
  if (!rule) return null;

  // Confidence grows with corroboration but is capped below 1: a sender rule is
  // a strong prior, not a certainty, and people do change what they email about.
  const corroboration = rule.confirmations * 2 + Math.min(rule.hits, 20) / 10;
  const confidence = Math.min(0.97, 0.72 + corroboration * 0.05);

  return {
    categoryId: rule.categoryId,
    confidence,
    reason:
      rule.confirmations > 0
        ? `You've filed ${rule.pattern} here ${rule.confirmations} time(s).`
        : `Learned from previous mail from ${rule.pattern}.`,
  };
}

/**
 * Add or reinforce a rule.
 *
 * `confirmed` distinguishes an explicit user correction from our own inference.
 * Only confirmations count toward promotion into a native Outlook rule, because
 * promoting a guess would bake a mistake into the mailbox where it keeps
 * applying itself long after the add-in is closed.
 */
export function upsertSenderRule(
  rules: SenderRule[],
  pattern: string,
  categoryId: string,
  confirmed: boolean,
): SenderRule[] {
  const key = normalizeSender(pattern);
  const next = [...rules];
  const idx = next.findIndex((r) => r.pattern === key);

  if (idx === -1) {
    next.push({
      pattern: key,
      categoryId,
      hits: confirmed ? 0 : 1,
      confirmations: confirmed ? 1 : 0,
      promoted: false,
      createdAt: new Date().toISOString(),
    });
    return next;
  }

  const existing = next[idx] as SenderRule;
  if (existing.categoryId !== categoryId) {
    // The user changed their mind about this sender. Reset the evidence rather
    // than averaging two contradictory histories, and un-promote so the stale
    // native Outlook rule gets rewritten on the next promotion pass.
    next[idx] = {
      ...existing,
      categoryId,
      hits: 0,
      confirmations: confirmed ? 1 : 0,
      promoted: false,
    };
    return next;
  }

  next[idx] = {
    ...existing,
    hits: existing.hits + (confirmed ? 0 : 1),
    confirmations: existing.confirmations + (confirmed ? 1 : 0),
  };
  return next;
}

/**
 * Fold a user correction into the rule set.
 *
 * Deliberately learns the exact address, never the domain. Inferring
 * "@gmail.com means Grants" from one grantseeker would be catastrophic;
 * domain rules are only ever created explicitly by the user in the UI.
 */
export function learnFromCorrection(rules: SenderRule[], correction: Correction): SenderRule[] {
  return upsertSenderRule(rules, correction.sender, correction.toCategoryId, true);
}

/**
 * Seed rules from mail the user has already categorized.
 *
 * Her existing categories *are* a labelled training set, so first run should
 * mine them silently before asking her anything. Only senders that map
 * unambiguously to a single category are taken - a sender seen under two
 * different categories teaches us nothing reliable and is left to the LLM.
 */
export function bootstrapSenderRules(
  history: MailSummary[],
  taxonomy: Category[],
  existing: SenderRule[],
): SenderRule[] {
  const nameToId = new Map(
    taxonomy.map((c) => [c.name.trim().toLowerCase(), c.id] as const),
  );

  const observed = new Map<string, Set<string>>();
  for (const mail of history) {
    const sender = normalizeSender(mail.from);
    if (!sender) continue;
    for (const catName of mail.categories) {
      const id = nameToId.get(catName.trim().toLowerCase());
      if (!id || id === NEEDS_REVIEW_ID) continue;
      const set = observed.get(sender) ?? new Set<string>();
      set.add(id);
      observed.set(sender, set);
    }
  }

  let rules = existing;
  for (const [sender, categories] of observed) {
    if (categories.size !== 1) continue;
    const [only] = [...categories];
    if (!only) continue;
    if (rules.some((r) => r.pattern === sender)) continue;
    rules = upsertSenderRule(rules, sender, only, true);
  }
  return rules;
}

/** Senders that have earned a native Outlook rule but don't have one yet. */
export function promotionCandidates(rules: SenderRule[], threshold: number): SenderRule[] {
  return rules.filter(
    (r) => !r.promoted && r.categoryId !== NEEDS_REVIEW_ID && r.confirmations >= threshold,
  );
}
