/**
 * Offline test harness.
 *
 * Two modes:
 *   npm test        - deterministic logic checks, no network, no mailbox.
 *   npm run test:llm - additionally measures real classification accuracy
 *                      against Gemini. Needs GEMINI_API_KEY in the environment.
 *
 * The offline mode exists because the target mailbox is not available to us: all
 * the logic that decides what gets labelled, what gets learned, and what gets
 * promoted has to be verifiable without an inbox.
 */

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import type { Correction, MailSummary, PersistedState, SenderRule } from '../src/types.js';
import { defaultState, NEEDS_REVIEW_ID, SEED_TAXONOMY } from '../src/taxonomy.js';
import {
  bootstrapSenderRules,
  classifyByRule,
  domainOf,
  learnFromCorrection,
  matchSender,
  promotionCandidates,
  upsertSenderRule,
} from '../src/classify/rules.js';
import { gate, categoryToApply, shouldAutoApply, updateAgreement } from '../src/classify/confidence.js';
import { detectCorrections, needsClassification } from '../src/engine.js';
import { encodeWithinBudget, stateFootprint } from '../src/state.js';
import { buildPrompt, classifyBatch, LLM_BATCH_SIZE } from '../src/classify/llm.js';

const here = dirname(fileURLToPath(import.meta.url));

interface Fixture {
  from: string;
  fromName: string;
  subject: string;
  preview: string;
  expected: string;
  hasAttachments?: boolean;
  listId?: string;
}

const fixtures: Fixture[] = JSON.parse(
  readFileSync(join(here, 'fixtures', 'emails.json'), 'utf8'),
) as Fixture[];

// ---------------------------------------------------------------------------
// Tiny assertion harness
// ---------------------------------------------------------------------------

let passed = 0;
const failures: string[] = [];

function check(name: string, fn: () => void): void {
  try {
    fn();
    passed++;
  } catch (err) {
    failures.push(`${name}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

async function checkAsync(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    passed++;
  } catch (err) {
    failures.push(`${name}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

function assert(condition: unknown, message: string): void {
  if (!condition) throw new Error(message);
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) throw new Error(`${message} (got ${String(actual)}, want ${String(expected)})`);
}

function toMail(f: Fixture, index: number): MailSummary {
  const mail: MailSummary = {
    id: `msg-${index}`,
    from: f.from,
    fromName: f.fromName,
    subject: f.subject,
    received: '2026-07-01T12:00:00Z',
    hasAttachments: f.hasAttachments ?? false,
    categories: [],
    preview: f.preview,
  };
  if (f.listId) mail.listId = f.listId;
  return mail;
}

const mails = fixtures.map(toMail);

// ---------------------------------------------------------------------------
// Layer 1: sender rules
// ---------------------------------------------------------------------------

check('domainOf extracts the domain with its @', () => {
  assertEqual(domainOf('a.b@example.org'), '@example.org', 'domain');
  assertEqual(domainOf('malformed'), '', 'no @ yields empty');
});

check('an exact sender rule beats a domain rule', () => {
  let rules: SenderRule[] = [];
  rules = upsertSenderRule(rules, '@coloradogives.org', 'finance', true);
  rules = upsertSenderRule(rules, 'rep@coloradogives.org', 'partners', true);

  const hit = matchSender(rules, 'rep@coloradogives.org');
  assertEqual(hit?.categoryId, 'partners', 'specific rule wins');

  const other = matchSender(rules, 'noreply@coloradogives.org');
  assertEqual(other?.categoryId, 'finance', 'domain rule catches the rest');
});

check('sender matching is case-insensitive', () => {
  const rules = upsertSenderRule([], 'Board@Example.ORG', 'board', true);
  assertEqual(matchSender(rules, 'board@example.org')?.categoryId, 'board', 'normalized');
});

check('confirmations raise confidence, and it never reaches certainty', () => {
  let rules = upsertSenderRule([], 'x@y.org', 'grants', true);
  const first = classifyByRule(rules, { ...(mails[0] as MailSummary), from: 'x@y.org' });
  for (let i = 0; i < 20; i++) rules = upsertSenderRule(rules, 'x@y.org', 'grants', true);
  const later = classifyByRule(rules, { ...(mails[0] as MailSummary), from: 'x@y.org' });

  assert(first && later, 'both classify');
  assert((later as { confidence: number }).confidence > (first as { confidence: number }).confidence, 'grows');
  assert((later as { confidence: number }).confidence < 1, 'capped below 1');
});

check('changing a sender category resets evidence and un-promotes', () => {
  let rules = upsertSenderRule([], 'donor@x.org', 'donors', true);
  rules = upsertSenderRule(rules, 'donor@x.org', 'donors', true);
  rules = rules.map((r) => ({ ...r, promoted: true }));

  rules = upsertSenderRule(rules, 'donor@x.org', 'events', true);
  const rule = rules[0] as SenderRule;
  assertEqual(rule.categoryId, 'events', 'category switched');
  assertEqual(rule.confirmations, 1, 'evidence reset');
  assertEqual(rule.promoted, false, 'stale native rule will be rewritten');
});

check('a correction learns the exact address, never the domain', () => {
  const correction: Correction = {
    sender: 'someone@gmail.com',
    subject: 'LOI attached',
    fromCategoryId: 'donors',
    toCategoryId: 'grants',
    at: '2026-07-01T00:00:00Z',
  };
  const rules = learnFromCorrection([], correction);
  assertEqual(rules.length, 1, 'one rule');
  assertEqual(rules[0]?.pattern, 'someone@gmail.com', 'exact address');
  assert(
    !rules.some((r) => r.pattern.startsWith('@')),
    'a single grantseeker must never make @gmail.com mean Grants',
  );
});

check('only confirmed senders become promotion candidates', () => {
  let rules = upsertSenderRule([], 'auto@x.org', 'finance', false);
  for (let i = 0; i < 10; i++) rules = upsertSenderRule(rules, 'auto@x.org', 'finance', false);
  assertEqual(promotionCandidates(rules, 3).length, 0, 'inferred hits never promote');

  for (let i = 0; i < 3; i++) rules = upsertSenderRule(rules, 'auto@x.org', 'finance', true);
  assertEqual(promotionCandidates(rules, 3).length, 1, 'confirmations do');
});

check('Needs Review is never promoted to a native rule', () => {
  let rules: SenderRule[] = [];
  for (let i = 0; i < 5; i++) rules = upsertSenderRule(rules, 'a@b.org', NEEDS_REVIEW_ID, true);
  assertEqual(promotionCandidates(rules, 3).length, 0, 'excluded');
});

check('bootstrap mines unambiguous senders and skips contradictory ones', () => {
  const history: MailSummary[] = [
    { ...(mails[0] as MailSummary), from: 'clear@x.org', categories: ['Grants'] },
    { ...(mails[1] as MailSummary), from: 'clear@x.org', categories: ['Grants'] },
    { ...(mails[2] as MailSummary), from: 'mixed@y.org', categories: ['Grants'] },
    { ...(mails[3] as MailSummary), from: 'mixed@y.org', categories: ['Events'] },
    { ...(mails[4] as MailSummary), from: 'unknown@z.org', categories: ['Some Other Label'] },
  ];
  const rules = bootstrapSenderRules(history, SEED_TAXONOMY, []);
  assertEqual(rules.length, 1, 'only the unambiguous sender');
  assertEqual(rules[0]?.pattern, 'clear@x.org', 'the right one');
});

// ---------------------------------------------------------------------------
// Layer 3: the gate
// ---------------------------------------------------------------------------

check('low confidence is gated to Needs Review', () => {
  const decision = gate('m1', [{ categoryId: 'grants', confidence: 0.4, reason: '' }], 0.65);
  assert(decision.gated, 'gated');
  assertEqual(categoryToApply(decision), NEEDS_REVIEW_ID, 'writes Needs Review');
});

check('a narrow margin between the top two is gated even when scores are high', () => {
  const decision = gate(
    'm2',
    [
      { categoryId: 'grants', confidence: 0.88, reason: '' },
      { categoryId: 'partners', confidence: 0.85, reason: '' },
    ],
    0.65,
  );
  assert(decision.gated, 'a coin flip between two categories is not a decision');
});

check('a clear winner is applied', () => {
  const decision = gate(
    'm3',
    [
      { categoryId: 'grants', confidence: 0.91, reason: '' },
      { categoryId: 'partners', confidence: 0.2, reason: '' },
    ],
    0.65,
  );
  assert(!decision.gated, 'not gated');
  assertEqual(categoryToApply(decision), 'grants', 'applies the winner');
});

check('the gate keeps at most three ranked alternatives and drops Needs Review', () => {
  const decision = gate(
    'm4',
    [
      { categoryId: 'a', confidence: 0.9, reason: '' },
      { categoryId: NEEDS_REVIEW_ID, confidence: 0.8, reason: '' },
      { categoryId: 'b', confidence: 0.5, reason: '' },
      { categoryId: 'c', confidence: 0.4, reason: '' },
      { categoryId: 'd', confidence: 0.3, reason: '' },
    ],
    0.65,
  );
  assertEqual(decision.ranked.length, 3, 'three shown');
  assert(!decision.ranked.some((r) => r.categoryId === NEEDS_REVIEW_ID), 'gate bucket excluded');
});

check('an empty suggestion list resolves to unresolved, not a crash', () => {
  const decision = gate('m5', [], 0.65);
  assertEqual(decision.source, 'unresolved', 'unresolved');
  assertEqual(categoryToApply(decision), NEEDS_REVIEW_ID, 'Needs Review');
});

// ---------------------------------------------------------------------------
// Autonomy
// ---------------------------------------------------------------------------

check('graduated autonomy withholds auto-labelling until it has earned it', () => {
  const state = defaultState();
  state.settings.autonomy = 'graduated';

  state.settings.agreementRate = 0.5;
  assertEqual(shouldAutoApply(state.settings), false, 'not yet');

  state.settings.agreementRate = 0.9;
  assertEqual(shouldAutoApply(state.settings), true, 'now');
});

check('suggest mode never auto-applies, auto mode always does', () => {
  const state = defaultState();
  state.settings.agreementRate = 1;
  state.settings.autonomy = 'suggest';
  assertEqual(shouldAutoApply(state.settings), false, 'suggest');
  state.settings.autonomy = 'auto';
  state.settings.agreementRate = 0;
  assertEqual(shouldAutoApply(state.settings), true, 'auto');
});

check('agreement rate reacts to recent behaviour', () => {
  let rate = 0.9;
  for (let i = 0; i < 30; i++) rate = updateAgreement(rate, false);
  assert(rate < 0.1, `sustained disagreement should collapse the rate, got ${rate}`);
});

// ---------------------------------------------------------------------------
// Correction detection
// ---------------------------------------------------------------------------

check('a category changed in Outlook is detected as a correction', () => {
  const mail: MailSummary[] = [
    {
      ...(mails[0] as MailSummary),
      from: 'x@y.org',
      categories: ['Events'],
      assigned: { categoryId: 'donors', confidence: 0.8, at: '' },
    },
  ];
  const corrections = detectCorrections(mail, SEED_TAXONOMY);
  assertEqual(corrections.length, 1, 'one correction');
  assertEqual(corrections[0]?.fromCategoryId, 'donors', 'from');
  assertEqual(corrections[0]?.toCategoryId, 'events', 'to');
});

check('an unchanged label is not a correction', () => {
  const mail: MailSummary[] = [
    {
      ...(mails[0] as MailSummary),
      categories: ['Donors & Gifts'],
      assigned: { categoryId: 'donors', confidence: 0.8, at: '' },
    },
  ];
  assertEqual(detectCorrections(mail, SEED_TAXONOMY).length, 0, 'no correction');
});

check('a removed label is not treated as a correction', () => {
  const mail: MailSummary[] = [
    {
      ...(mails[0] as MailSummary),
      categories: [],
      assigned: { categoryId: 'donors', confidence: 0.8, at: '' },
    },
  ];
  assertEqual(
    detectCorrections(mail, SEED_TAXONOMY).length,
    0,
    '"not this" without "but that" teaches nothing',
  );
});

check('multi-labelling is not mistaken for a correction', () => {
  const mail: MailSummary[] = [
    {
      ...(mails[0] as MailSummary),
      categories: ['Events', 'Board & Governance'],
      assigned: { categoryId: 'donors', confidence: 0.8, at: '' },
    },
  ];
  assertEqual(detectCorrections(mail, SEED_TAXONOMY).length, 0, 'ambiguous, so ignored');
});

check('mail we never touched yields no corrections', () => {
  const mail: MailSummary[] = [{ ...(mails[0] as MailSummary), categories: ['Events'] }];
  assertEqual(detectCorrections(mail, SEED_TAXONOMY).length, 0, 'no provenance, no claim');
});

// ---------------------------------------------------------------------------
// Work selection
// ---------------------------------------------------------------------------

check('unlabelled mail and Needs Review mail are reclassified; settled mail is left alone', () => {
  const mail: MailSummary[] = [
    { ...(mails[0] as MailSummary), id: 'a', categories: [] },
    { ...(mails[1] as MailSummary), id: 'b', categories: ['⚠ Needs Review'] },
    { ...(mails[2] as MailSummary), id: 'c', categories: ['Grants'] },
    { ...(mails[3] as MailSummary), id: 'd', categories: ['Someone Elses Label'] },
  ];
  const ids = needsClassification(mail, SEED_TAXONOMY).map((m) => m.id);
  assert(ids.includes('a'), 'unlabelled');
  assert(ids.includes('b'), 'Needs Review is retried');
  assert(!ids.includes('c'), 'already sorted');
  assert(ids.includes('d'), 'a foreign label is not our decision');
});

// ---------------------------------------------------------------------------
// Persistence budget
// ---------------------------------------------------------------------------

check('state is trimmed to fit the roaming settings budget', () => {
  const state: PersistedState = defaultState();
  for (let i = 0; i < 5000; i++) {
    state.senderRules.push({
      pattern: `sender${i}@somewhat-long-domain-name-${i}.org`,
      categoryId: 'grants',
      hits: i % 7,
      confirmations: i % 3,
      promoted: false,
      createdAt: '2026-07-01T00:00:00.000Z',
    });
  }

  const { trimmed } = encodeWithinBudget(state);
  const { bytes, limit } = stateFootprint(trimmed);
  assert(bytes <= limit, `trimmed to ${bytes} bytes, limit ${limit}`);
  assertEqual(trimmed.taxonomy.length, state.taxonomy.length, 'taxonomy is never sacrificed');
  assertEqual(trimmed.settings.model, state.settings.model, 'settings survive');
});

check('trimming keeps the most valuable sender rules', () => {
  const state = defaultState();
  for (let i = 0; i < 2000; i++) {
    state.senderRules.push({
      pattern: `low${i}@example.org`,
      categoryId: 'sector',
      hits: 1,
      confirmations: 0,
      promoted: false,
      createdAt: '2026-07-01T00:00:00.000Z',
    });
  }
  state.senderRules.push({
    pattern: 'precious@example.org',
    categoryId: 'board',
    hits: 40,
    confirmations: 25,
    promoted: true,
    createdAt: '2026-07-01T00:00:00.000Z',
  });

  const { trimmed } = encodeWithinBudget(state);
  assert(
    trimmed.senderRules.some((r) => r.pattern === 'precious@example.org'),
    'a heavily confirmed rule must survive eviction',
  );
});

// ---------------------------------------------------------------------------
// Prompt construction
// ---------------------------------------------------------------------------

check('the prompt carries categories, examples, and the messages', () => {
  const corrections: Correction[] = [
    {
      sender: 'known@x.org',
      subject: 'Prior thing',
      fromCategoryId: 'donors',
      toCategoryId: 'events',
      at: '',
    },
  ];
  const prompt = buildPrompt(mails.slice(0, 3), SEED_TAXONOMY, corrections, true);

  assert(prompt.includes('grants'), 'category ids present');
  assert(prompt.includes('Durango Wine Experience'), 'descriptions present');
  assert(prompt.includes('known@x.org'), 'few-shot examples present');
  assert(prompt.includes('[0]') && prompt.includes('[2]'), 'messages are numbered for ref matching');
  assert(!prompt.includes(NEEDS_REVIEW_ID + '"'), 'the gate bucket is not offered as a choice');
});

check('metadata-only sharing keeps the body out of the prompt', () => {
  const withBody = buildPrompt(mails.slice(0, 5), SEED_TAXONOMY, [], true);
  const withoutBody = buildPrompt(mails.slice(0, 5), SEED_TAXONOMY, [], false);

  const secret = 'appreciated stock rather than cash';
  assert(withBody.includes(secret), 'full sharing includes the preview');
  assert(!withoutBody.includes(secret), 'metadata-only must never leak message text');
  assert(withoutBody.includes('subject:'), 'subject still present');
});

// ---------------------------------------------------------------------------
// Fixture sanity
// ---------------------------------------------------------------------------

check('every fixture targets a real category', () => {
  const ids = new Set(SEED_TAXONOMY.map((c) => c.id));
  for (const f of fixtures) {
    assert(ids.has(f.expected), `unknown expected category "${f.expected}" for "${f.subject}"`);
  }
});

check('the fixture set covers every category', () => {
  const covered = new Set(fixtures.map((f) => f.expected));
  const missing = SEED_TAXONOMY.filter(
    (c) => c.id !== NEEDS_REVIEW_ID && !covered.has(c.id),
  ).map((c) => c.id);
  assertEqual(missing.length, 0, `categories with no fixtures: ${missing.join(', ')}`);
});

// ---------------------------------------------------------------------------
// Promotion into native Outlook rules
// ---------------------------------------------------------------------------

/**
 * Minimal Graph stub covering just the messageRules surface, so promotion can be
 * tested without a mailbox.
 */
interface StubRule {
  id: string;
  displayName?: string;
  sequence?: number;
  isEnabled?: boolean;
  conditions?: { senderContains?: string[] };
  actions?: { assignCategories?: string[]; stopProcessingRules?: boolean };
}

function stubGraph(initial: Omit<StubRule, 'id'>[]) {
  let rules: StubRule[] = initial.map((r, i) => ({ id: `rule-${i}`, ...r }));
  let seq = rules.length;
  const realFetch = globalThis.fetch;

  globalThis.fetch = (async (url: string | URL | Request, init?: RequestInit) => {
    const href = String(url);
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null;
    const respond = (payload: unknown, status = 200) =>
      new Response(JSON.stringify(payload), {
        status,
        headers: { 'Content-Type': 'application/json' },
      });

    if (href.includes('/messageRules/')) {
      const id = href.split('/messageRules/')[1] ?? '';
      if (method === 'DELETE') {
        rules = rules.filter((r) => r.id !== id);
        return new Response(null, { status: 204 });
      }
      if (method === 'PATCH' && body) {
        rules = rules.map((r) => (r.id === id ? { ...r, ...body } : r));
        return respond({});
      }
    }
    if (href.includes('/messageRules')) {
      if (method === 'POST' && body) {
        const rule = { id: `rule-${seq++}`, ...body } as StubRule;
        rules.push(rule);
        return respond(rule);
      }
      return respond({ value: rules });
    }
    return respond({ value: [] });
  }) as typeof globalThis.fetch;

  return {
    rules: () => rules,
    restore: () => {
      globalThis.fetch = realFetch;
    },
  };
}

await (async () => {
  const { promoteRules } = await import('../src/promote.js');

  await checkAsync('a confirmed sender becomes a native Outlook rule', async () => {
    const graph = stubGraph([]);
    try {
      const rules: SenderRule[] = [
        {
          pattern: 'donor@example.org',
          categoryId: 'donors',
          hits: 4,
          confirmations: 3,
          promoted: false,
          createdAt: '',
        },
      ];
      const result = await promoteRules('t', rules, SEED_TAXONOMY, 3);
      assertEqual(result.promotedPatterns.length, 1, 'one promoted');

      const native = graph.rules();
      assertEqual(native.length, 1, 'one native rule');
      const rule = native[0];
      assertEqual(rule?.actions?.assignCategories?.[0], 'Donors & Gifts', 'assigns the category');
      assert(rule?.conditions?.senderContains?.includes('donor@example.org'), 'matches the sender');
    } finally {
      graph.restore();
    }
  });

  await checkAsync('a sender whose category changed is evicted from its old rule', async () => {
    const graph = stubGraph([
      {
        displayName: 'Inbox Steward: Donors & Gifts',
        sequence: 1,
        isEnabled: true,
        conditions: { senderContains: ['mover@example.org', 'stayer@example.org'] },
        actions: { assignCategories: ['Donors & Gifts'] },
      },
    ]);
    try {
      // The user has since refiled this sender under Events.
      const rules: SenderRule[] = [
        {
          pattern: 'mover@example.org',
          categoryId: 'events',
          hits: 0,
          confirmations: 3,
          promoted: false,
          createdAt: '',
        },
      ];
      await promoteRules('t', rules, SEED_TAXONOMY, 3);

      const native = graph.rules();
      const donors = native.find((r) => r.actions?.assignCategories?.[0] === 'Donors & Gifts');
      const events = native.find((r) => r.actions?.assignCategories?.[0] === 'Events');

      assert(events?.conditions?.senderContains?.includes('mover@example.org'), 'added to Events');
      assert(
        !donors?.conditions?.senderContains?.includes('mover@example.org'),
        'left in the Donors rule it would arrive double-labelled forever',
      );
      assert(
        donors?.conditions?.senderContains?.includes('stayer@example.org'),
        'unrelated senders in that rule must be untouched',
      );
    } finally {
      graph.restore();
    }
  });

  await checkAsync('a rule emptied by eviction is deleted, not left matching nothing', async () => {
    const graph = stubGraph([
      {
        displayName: 'Inbox Steward: Donors & Gifts',
        sequence: 1,
        isEnabled: true,
        conditions: { senderContains: ['only@example.org'] },
        actions: { assignCategories: ['Donors & Gifts'] },
      },
    ]);
    try {
      await promoteRules(
        't',
        [
          {
            pattern: 'only@example.org',
            categoryId: 'events',
            hits: 0,
            confirmations: 3,
            promoted: false,
            createdAt: '',
          },
        ],
        SEED_TAXONOMY,
        3,
      );
      assert(
        !graph.rules().some((r) => r.actions?.assignCategories?.[0] === 'Donors & Gifts'),
        'the emptied rule should be gone',
      );
    } finally {
      graph.restore();
    }
  });

  await checkAsync('promotion is idempotent', async () => {
    const graph = stubGraph([]);
    try {
      const rules: SenderRule[] = [
        {
          pattern: 'donor@example.org',
          categoryId: 'donors',
          hits: 4,
          confirmations: 3,
          promoted: false,
          createdAt: '',
        },
      ];
      await promoteRules('t', rules, SEED_TAXONOMY, 3);
      await promoteRules('t', rules, SEED_TAXONOMY, 3);
      assertEqual(graph.rules().length, 1, 'no duplicate rule on a second pass');
    } finally {
      graph.restore();
    }
  });

  await checkAsync('senders below the threshold are not promoted', async () => {
    const graph = stubGraph([]);
    try {
      await promoteRules(
        't',
        [
          {
            pattern: 'maybe@example.org',
            categoryId: 'donors',
            hits: 20,
            confirmations: 1,
            promoted: false,
            createdAt: '',
          },
        ],
        SEED_TAXONOMY,
        3,
      );
      assertEqual(graph.rules().length, 0, 'one confirmation is not enough to alter the mailbox');
    } finally {
      graph.restore();
    }
  });
})();

// ---------------------------------------------------------------------------
// Optional: live accuracy measurement
// ---------------------------------------------------------------------------

async function measureAccuracy(): Promise<void> {
  const apiKey = process.env.GEMINI_API_KEY;
  if (!apiKey) {
    console.log('\nSkipping accuracy run: set GEMINI_API_KEY to enable it.');
    return;
  }

  const state = defaultState();
  state.settings.geminiApiKey = apiKey;

  console.log(`\nClassifying ${mails.length} fixtures with ${state.settings.model}…`);

  let top1 = 0;
  let top3 = 0;
  let gated = 0;
  let scored = 0;
  const confusion: string[] = [];

  for (let i = 0; i < mails.length; i += LLM_BATCH_SIZE) {
    const batch = mails.slice(i, i + LLM_BATCH_SIZE);
    const results = await classifyBatch(batch, state.taxonomy, [], state.settings);

    for (const mail of batch) {
      const index = Number(mail.id.replace('msg-', ''));
      const expected = (fixtures[index] as Fixture).expected;
      const ranked = results.get(mail.id) ?? [];
      scored++;

      const decision = gate(mail.id, ranked, state.settings.confidenceThreshold);
      if (decision.gated) gated++;

      const ids = ranked.map((r) => r.categoryId);
      if (ids[0] === expected) top1++;
      else confusion.push(`  ${expected} -> ${ids[0] ?? 'none'}  "${mail.subject.slice(0, 58)}"`);
      if (ids.includes(expected)) top3++;
    }
  }

  const pct = (n: number) => `${((n / scored) * 100).toFixed(1)}%`;
  console.log(`\nAccuracy over ${scored} messages`);
  console.log(`  top-1 ............ ${pct(top1)}  (target >= 80%)`);
  console.log(`  top-3 ............ ${pct(top3)}  (target >= 90%)`);
  console.log(`  sent to review ... ${pct(gated)}`);

  if (confusion.length > 0) {
    console.log('\nTop-1 misses (expected -> predicted):');
    for (const line of confusion) console.log(line);
  }

  // Misses that land in Needs Review are acceptable; misses that land in a
  // confident wrong category are the expensive kind.
  if (top1 / scored < 0.8) {
    console.log('\n⚠ top-1 below target. Tighten the category descriptions in src/taxonomy.ts.');
  }
}

// ---------------------------------------------------------------------------

console.log(`${passed} check(s) passed.`);
if (failures.length > 0) {
  console.log(`\n${failures.length} FAILED:`);
  for (const f of failures) console.log(`  ✗ ${f}`);
}

if (process.argv.includes('--llm')) {
  await measureAccuracy();
}

process.exit(failures.length > 0 ? 1 : 0);
