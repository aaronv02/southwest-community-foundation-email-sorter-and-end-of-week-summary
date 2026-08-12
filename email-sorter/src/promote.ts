import type { Category, SenderRule } from './types.js';
import {
  createMessageRule,
  deleteMessageRule,
  listMessageRules,
  updateMessageRule,
  type MessageRule,
} from './graph.js';
import { promotionCandidates } from './classify/rules.js';
import { categoryById } from './taxonomy.js';

/**
 * Promotion: turning what the add-in learned into native Outlook rules.
 *
 * This is the answer to the platform's central limitation. Outlook add-ins have
 * no "message received" event - the entire event surface is compose/send/read
 * oriented - so an add-in can only ever sort while it is open. Native Exchange
 * rules, by contrast, run server-side on delivery, sync to every client, and
 * cost nothing to operate.
 *
 * So once the add-in is confident about a sender, it writes that knowledge into
 * a real Outlook rule and stops needing to be involved. Mail from that sender
 * arrives already categorized whether or not anyone ever opens the taskpane
 * again. The intelligent layer teaches the always-on layer, and gradually works
 * itself out of a job.
 *
 * Only user-CONFIRMED senders are ever promoted. A native rule keeps applying
 * itself indefinitely and is invisible unless you go looking, so baking in a
 * guess would be a mistake that quietly compounds.
 */

/** Prefix identifying rules we own, so we never touch the user's own rules. */
const RULE_PREFIX = 'Inbox Steward:';

/**
 * Exchange caps total rule data at ~256 KB per mailbox. We stay well under it:
 * the user's own rules share that quota, and hitting the ceiling produces
 * confusing failures rather than a clean error.
 */
const RULE_QUOTA_BYTES = 256 * 1024;
const OUR_BUDGET_BYTES = 160 * 1024;

/**
 * Senders per rule. Consolidating many senders into one rule per category is
 * what makes the quota go far - one rule with 60 senders costs far less than 60
 * rules with one each.
 */
const MAX_SENDERS_PER_RULE = 60;

export interface PromotionResult {
  created: string[];
  updated: string[];
  /** Sender patterns that now have native coverage. */
  promotedPatterns: string[];
  /** True when we stopped early to stay inside the rule quota. */
  quotaLimited: boolean;
  bytesUsed: number;
  note?: string;
}

function ruleName(category: Category): string {
  return `${RULE_PREFIX} ${category.name}`;
}

function isOurs(rule: MessageRule): boolean {
  return rule.displayName?.startsWith(RULE_PREFIX) ?? false;
}

/** Rough on-the-wire size of a rule, for quota accounting. */
function ruleBytes(rule: Pick<MessageRule, 'displayName' | 'conditions' | 'actions'>): number {
  return new TextEncoder().encode(JSON.stringify(rule)).length;
}

/**
 * Push confident sender rules into native Outlook rules.
 *
 * Returns the patterns that were successfully covered; the caller marks those
 * `promoted` in state. Nothing is marked promoted unless Graph confirmed the
 * write, so a failure here simply means we try again next run.
 */
export async function promoteRules(
  token: string,
  senderRules: SenderRule[],
  taxonomy: Category[],
  promoteThreshold: number,
): Promise<PromotionResult> {
  const candidates = promotionCandidates(senderRules, promoteThreshold);
  const existing = await listMessageRules(token);

  const ours = existing.filter(isOurs);
  const theirBytes = existing
    .filter((r) => !isOurs(r))
    .reduce((sum, r) => sum + ruleBytes(r), 0);
  let ourBytes = ours.reduce((sum, r) => sum + ruleBytes(r), 0);

  const result: PromotionResult = {
    created: [],
    updated: [],
    promotedPatterns: [],
    quotaLimited: false,
    bytesUsed: theirBytes + ourBytes,
  };

  if (candidates.length === 0) return result;

  // Evict senders whose category has since changed BEFORE adding them anywhere.
  // Without this, a sender that moves from Donors to Events stays listed in the
  // old rule as well, and every future message from them arrives carrying both
  // labels - a wrong label that silently reapplies itself forever.
  const survivors = await evictStaleSenders(token, ours, candidates, taxonomy);
  ours.length = 0;
  ours.push(...survivors);

  // Highest sequence wins ordering; append after everything that exists so we
  // never pre-empt a rule the user wrote themselves.
  let nextSequence = existing.reduce((max, r) => Math.max(max, r.sequence ?? 0), 0) + 1;

  const byCategory = new Map<string, SenderRule[]>();
  for (const candidate of candidates) {
    const list = byCategory.get(candidate.categoryId) ?? [];
    list.push(candidate);
    byCategory.set(candidate.categoryId, list);
  }

  for (const [categoryId, rules] of byCategory) {
    const category = categoryById(taxonomy, categoryId);
    if (!category) continue;

    const name = ruleName(category);
    const mine = ours.filter((r) => r.displayName === name);

    // senderContains handles both shapes we learn: a full address matches that
    // sender, and an "@domain" pattern matches everyone there. fromAddresses
    // would need resolvable directory objects and can't express a domain.
    const alreadyCovered = new Set(
      mine.flatMap((r) => (r.conditions?.senderContains ?? []).map((s) => s.toLowerCase())),
    );
    const toAdd = rules
      .map((r) => r.pattern)
      .filter((p) => !alreadyCovered.has(p.toLowerCase()));
    if (toAdd.length === 0) {
      // Native coverage already exists - record it so state stops re-trying.
      result.promotedPatterns.push(...rules.map((r) => r.pattern));
      continue;
    }

    // Try to extend an existing rule of ours that still has room.
    const extendable = mine.find(
      (r) => (r.conditions?.senderContains?.length ?? 0) + toAdd.length <= MAX_SENDERS_PER_RULE,
    );

    if (extendable) {
      const merged = [...(extendable.conditions?.senderContains ?? []), ...toAdd];
      const projected: Pick<MessageRule, 'displayName' | 'conditions' | 'actions'> = {
        displayName: name,
        conditions: { senderContains: merged },
        actions: { assignCategories: [category.name] },
      };
      const delta = ruleBytes(projected) - ruleBytes(extendable);

      if (theirBytes + ourBytes + delta > OUR_BUDGET_BYTES) {
        result.quotaLimited = true;
        continue;
      }

      try {
        await updateMessageRule(token, extendable.id, {
          conditions: { senderContains: merged },
          actions: { assignCategories: [category.name] },
        });
        extendable.conditions = { senderContains: merged };
        ourBytes += delta;
        result.updated.push(name);
        result.promotedPatterns.push(...toAdd);
      } catch (err) {
        console.warn(`[promote] could not extend rule "${name}"`, err);
      }
      continue;
    }

    // Otherwise create a fresh rule, chunking if the batch is large.
    for (let i = 0; i < toAdd.length; i += MAX_SENDERS_PER_RULE) {
      const chunk = toAdd.slice(i, i + MAX_SENDERS_PER_RULE);
      const draft: Omit<MessageRule, 'id'> = {
        displayName: name,
        sequence: nextSequence,
        isEnabled: true,
        conditions: { senderContains: chunk },
        actions: {
          assignCategories: [category.name],
          // Leave the user's own downstream rules free to run.
          stopProcessingRules: false,
        },
      };

      const size = ruleBytes(draft);
      if (theirBytes + ourBytes + size > OUR_BUDGET_BYTES) {
        result.quotaLimited = true;
        break;
      }

      try {
        const created = await createMessageRule(token, draft);
        ours.push(created);
        nextSequence++;
        ourBytes += size;
        result.created.push(name);
        result.promotedPatterns.push(...chunk);
      } catch (err) {
        console.warn(`[promote] could not create rule "${name}"`, err);
        break;
      }
    }
  }

  result.bytesUsed = theirBytes + ourBytes;
  if (result.quotaLimited) {
    result.note =
      `Outlook's rule storage is nearly full (${Math.round(result.bytesUsed / 1024)} KB of ` +
      `${Math.round(RULE_QUOTA_BYTES / 1024)} KB used). Some senders will keep being sorted by ` +
      `the add-in instead of automatically. Removing unused rules in Outlook frees space.`;
  }

  return result;
}

/**
 * Remove senders from our rules that no longer point at that rule's category.
 *
 * Returns the surviving rules, with any emptied rule deleted outright rather
 * than left behind as a rule that matches nothing.
 */
async function evictStaleSenders(
  token: string,
  ours: MessageRule[],
  candidates: SenderRule[],
  taxonomy: Category[],
): Promise<MessageRule[]> {
  const desired = new Map<string, string | undefined>(
    candidates.map((c) => [c.pattern.toLowerCase(), categoryById(taxonomy, c.categoryId)?.name]),
  );

  const surviving: MessageRule[] = [];

  for (const rule of ours) {
    const senders = rule.conditions?.senderContains ?? [];
    const assigned = rule.actions?.assignCategories?.[0];

    const keep = senders.filter((sender) => {
      const wanted = desired.get(sender.toLowerCase());
      // Not a candidate this round, or already in the right rule.
      return wanted === undefined || wanted === assigned;
    });

    if (keep.length === senders.length) {
      surviving.push(rule);
      continue;
    }

    try {
      if (keep.length === 0) {
        await deleteMessageRule(token, rule.id);
        continue;
      }
      await updateMessageRule(token, rule.id, { conditions: { senderContains: keep } });
      rule.conditions = { ...rule.conditions, senderContains: keep };
      surviving.push(rule);
    } catch (err) {
      // If eviction fails, keep the rule as-is and skip promoting this sender
      // elsewhere on the next pass rather than risk a double label.
      console.warn(`[promote] could not evict stale senders from "${rule.displayName}"`, err);
      surviving.push(rule);
    }
  }

  return surviving;
}

/** Mark promoted patterns in state after a successful promotion pass. */
export function markPromoted(senderRules: SenderRule[], patterns: string[]): SenderRule[] {
  const done = new Set(patterns.map((p) => p.toLowerCase()));
  return senderRules.map((r) => (done.has(r.pattern.toLowerCase()) ? { ...r, promoted: true } : r));
}

/** Every native rule this add-in owns - for the "what has it automated?" view. */
export async function listOwnedRules(token: string): Promise<MessageRule[]> {
  const all = await listMessageRules(token);
  return all.filter(isOurs);
}
