// Dev-only: stands in for Office.js and Graph so the UI can be driven in a plain
// browser. `import.meta.env.DEV` is statically false in a production build, so
// the mock and its fixtures are tree-shaken out entirely.
if (import.meta.env.DEV) {
  await import('./dev-mock.js');
}

import type { Category, Decision, MailSummary, PersistedState } from './types.js';
import { loadState, saveState, stateFootprint, appendCorrection, recentCorpus } from './state.js';
import { getGraphToken, naaSupported, authMode, signedInAs, AuthUnavailableError } from './auth.js';
import { listRecentInbox, applyCategories, ensureMasterCategories, type CategoryUpdate } from './graph.js';
import { classifyAll, detectCorrections, needsClassification, type SortOutcome } from './engine.js';
import { shouldAutoApply, updateAgreement } from './classify/confidence.js';
import { learnFromCorrection, bootstrapSenderRules, upsertSenderRule } from './classify/rules.js';
import { promoteRules, markPromoted, listOwnedRules } from './promote.js';
import {
  NEEDS_REVIEW_ID,
  categoryById,
  selectableCategories,
  SEED_TAXONOMY,
} from './taxonomy.js';

/**
 * Taskpane controller.
 *
 * Deliberately vanilla DOM: the whole app has to fit in a static bundle served
 * from a CDN, and a framework would add weight without adding anything this UI
 * needs.
 */

const HISTORY_SCAN = 50;

interface Session {
  state: PersistedState;
  mail: Map<string, MailSummary>;
  decisions: Decision[];
  /** Category id currently written to each message, as far as we know. */
  applied: Map<string, string>;
  /** Message ids whose chips are expanded to the full category list. */
  expanded: Set<string>;
}

const session: Session = {
  state: loadState(),
  mail: new Map(),
  decisions: [],
  applied: new Map(),
  expanded: new Set(),
};

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

Office.onReady((info) => {
  const boot = el('boot');
  if (info.host !== Office.HostType.Outlook) {
    boot.textContent = 'Inbox Steward only runs inside Outlook.';
    return;
  }

  session.state = loadState();
  boot.hidden = true;
  el('app').hidden = false;

  bindTabs();
  bindReview();
  bindCategories();
  bindSettings();
  bindAutomation();

  renderSettings();
  renderCategories();
  renderDiagnostics();
  setStatus(
    session.state.settings.bootstrapped
      ? 'Ready.'
      : "First run — press Sort my inbox and it'll set up your labels.",
  );
});

// ---------------------------------------------------------------------------
// DOM helpers
// ---------------------------------------------------------------------------

function el(id: string): HTMLElement {
  const node = document.getElementById(id);
  if (!node) throw new Error(`Missing element #${id}`);
  return node;
}

function input(id: string): HTMLInputElement {
  return el(id) as HTMLInputElement;
}

function select(id: string): HTMLSelectElement {
  return el(id) as HTMLSelectElement;
}

function setStatus(text: string): void {
  el('status').textContent = text;
}

function showNote(text: string | undefined): void {
  const node = el('run-note');
  if (!text) {
    node.hidden = true;
    node.textContent = '';
    return;
  }
  node.hidden = false;
  node.textContent = text;
}

function bindTabs(): void {
  const tabs = [...document.querySelectorAll<HTMLButtonElement>('.tab')];
  for (const tab of tabs) {
    tab.addEventListener('click', () => {
      for (const other of tabs) other.classList.toggle('is-active', other === tab);
      for (const name of ['review', 'automation', 'categories', 'settings']) {
        el(`panel-${name}`).hidden = name !== tab.dataset.tab;
      }
      if (tab.dataset.tab === 'automation') void renderAutomation();
      if (tab.dataset.tab === 'settings') renderDiagnostics();
    });
  }
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

function bindReview(): void {
  el('sort').addEventListener('click', () => void runSort());
}

async function runSort(): Promise<void> {
  const button = el('sort') as HTMLButtonElement;
  button.disabled = true;
  showNote(undefined);

  try {
    setStatus('Signing in to your mailbox…');
    const token = await getGraphToken();

    // First run: create the master category list. Nothing can be labelled until
    // the categories exist in the mailbox, so this is not optional.
    if (!session.state.settings.bootstrapped) {
      setStatus('Setting up your labels…');
      await ensureMasterCategories(
        token,
        session.state.taxonomy.map((c) => ({ name: c.name, color: c.color })),
      );
    }

    setStatus('Reading your inbox…');
    const includePreview = session.state.settings.dataSharing === 'full';
    const mail = await listRecentInbox(token, HISTORY_SCAN, includePreview);
    session.mail = new Map(mail.map((m) => [m.id, m]));

    // Learn from anything she re-labelled in Outlook since the last run. This
    // happens before classification so the corrections inform this very run.
    const corrections = detectCorrections(mail, session.state.taxonomy);
    if (corrections.length > 0) {
      for (const correction of corrections) {
        session.state.senderRules = learnFromCorrection(session.state.senderRules, correction);
        session.state.recentCorrections.push(correction);
        session.state.settings.agreementRate = updateAgreement(
          session.state.settings.agreementRate,
          false,
        );
        void appendCorrection(correction);
      }
    }

    // First run also mines her existing categories as a training set - there is
    // free labelled data sitting in the mailbox and no reason to ignore it.
    if (!session.state.settings.bootstrapped) {
      session.state.senderRules = bootstrapSenderRules(
        mail,
        session.state.taxonomy,
        session.state.senderRules,
      );
      session.state.settings.bootstrapped = true;
    }

    const candidates = needsClassification(mail, session.state.taxonomy);
    if (candidates.length === 0) {
      session.decisions = [];
      renderDecisions();
      renderSummary(null, corrections.length, 0);
      setStatus('Nothing new to sort.');
      session.state = await saveState(session.state);
      return;
    }

    setStatus(`Sorting ${candidates.length} message(s)…`);
    const corpus = await recentCorpus(30);
    const primed = corpus.length > 0 ? corpus : session.state.recentCorrections.slice().reverse();
    const outcome = await classifyAll(candidates, session.state, primed);
    session.decisions = outcome.decisions;

    const auto = shouldAutoApply(session.state.settings);
    const writes: CategoryUpdate[] = [];

    for (const decision of outcome.decisions) {
      const mailItem = session.mail.get(decision.messageId);
      if (!mailItem) continue;

      if (decision.gated) {
        // Always write Needs Review, in every mode: an admitted unknown is
        // information, and it makes the backlog visible in Outlook itself.
        writes.push(buildWrite(mailItem, NEEDS_REVIEW_ID, 0));
        session.applied.set(decision.messageId, NEEDS_REVIEW_ID);
      } else if (auto) {
        const top = decision.ranked[0];
        if (!top) continue;
        writes.push(buildWrite(mailItem, top.categoryId, top.confidence));
        session.applied.set(decision.messageId, top.categoryId);
        // Counted as agreement unless she later overrules it, at which point
        // detectCorrections will subtract.
        session.state.settings.agreementRate = updateAgreement(
          session.state.settings.agreementRate,
          true,
        );
        // A label we applied ourselves reinforces the sender, but as a hit
        // rather than a confirmation, so it can never earn promotion alone.
        session.state.senderRules = upsertSenderRule(
          session.state.senderRules,
          mailItem.from,
          top.categoryId,
          false,
        );
      }
    }

    if (writes.length > 0) {
      setStatus(`Labelling ${writes.length} message(s)…`);
      const result = await applyCategories(token, writes);
      if (result.failed.length > 0) {
        showNote(`${result.failed.length} message(s) could not be labelled. They stay unchanged.`);
      }
    }

    // Hand newly-confident senders to Outlook so they stop needing us.
    const promotion = await promoteRules(
      token,
      session.state.senderRules,
      session.state.taxonomy,
      session.state.settings.promoteThreshold,
    );
    if (promotion.promotedPatterns.length > 0) {
      session.state.senderRules = markPromoted(
        session.state.senderRules,
        promotion.promotedPatterns,
      );
    }

    session.state = await saveState(session.state);

    renderDecisions();
    renderSummary(outcome, corrections.length, promotion.promotedPatterns.length);
    showNote(outcome.note ?? promotion.note);
    setStatus(auto ? 'Done.' : 'Done — pick a label on anything it got wrong.');
  } catch (err) {
    handleError(err);
  } finally {
    button.disabled = false;
  }
}

/**
 * Compute the category set to write.
 *
 * Graph PATCH on `categories` replaces the array wholesale, so any category the
 * user applied by hand that isn't ours must be carried across or it silently
 * disappears.
 */
function buildWrite(mail: MailSummary, categoryId: string, confidence: number): CategoryUpdate {
  const ourNames = new Set(session.state.taxonomy.map((c) => c.name.trim().toLowerCase()));
  const foreign = mail.categories.filter((n) => !ourNames.has(n.trim().toLowerCase()));
  const category = categoryById(session.state.taxonomy, categoryId);
  const next = category ? [...foreign, category.name] : foreign;
  return { messageId: mail.id, categories: next, provenance: { categoryId, confidence } };
}

function handleError(err: unknown): void {
  console.error(err);
  if (err instanceof AuthUnavailableError) {
    setStatus('Sign-in is not configured.');
    showNote(err.message);
    return;
  }
  const message = err instanceof Error ? err.message : String(err);
  setStatus('Something went wrong.');
  showNote(message);
}

// ---------------------------------------------------------------------------
// Review rendering
// ---------------------------------------------------------------------------

function renderSummary(
  outcome: SortOutcome | null,
  corrections: number,
  promoted: number,
): void {
  const node = el('summary');
  const bits: string[] = [];

  if (outcome) {
    if (outcome.resolvedByRule > 0) {
      bits.push(`${outcome.resolvedByRule} sorted instantly from senders it already knows`);
    }
    // Count by what actually happened rather than by subtraction: mail the LLM
    // never saw (no key, quota exhausted) must not be reported as classified.
    const byLlm = outcome.decisions.filter((d) => d.source === 'llm').length;
    const unresolved = outcome.decisions.filter((d) => d.source === 'unresolved').length;
    if (byLlm > 0) bits.push(`${byLlm} read and classified (${outcome.llmRequests} AI request(s))`);
    if (unresolved > 0) bits.push(`${unresolved} left for you to label`);
  }
  if (corrections > 0) bits.push(`learned from ${corrections} correction(s) you made in Outlook`);
  if (promoted > 0) bits.push(`${promoted} sender(s) handed off to Outlook's own rules`);

  node.hidden = bits.length === 0;
  node.textContent = bits.length > 0 ? `${bits.join(' · ')}.` : '';
}

function renderDecisions(): void {
  const list = el('decisions');
  list.textContent = '';
  el('review-empty').hidden = session.decisions.length > 0;

  for (const decision of session.decisions) {
    const mail = session.mail.get(decision.messageId);
    if (!mail) continue;
    list.append(renderDecision(decision, mail));
  }
}

function renderDecision(decision: Decision, mail: MailSummary): HTMLLIElement {
  const li = document.createElement('li');
  li.className = decision.gated ? 'decision is-gated' : 'decision';

  const subject = document.createElement('div');
  subject.className = 'decision-subject';
  subject.textContent = mail.subject;
  subject.title = mail.subject;
  li.append(subject);

  const from = document.createElement('div');
  from.className = 'decision-from';
  from.textContent = mail.fromName ? `${mail.fromName} · ${mail.from}` : mail.from;
  li.append(from);

  const top = decision.ranked[0];
  if (top) {
    const reason = document.createElement('div');
    reason.className = 'decision-reason';
    reason.textContent = top.reason;
    li.append(reason);
  }

  li.append(renderChoices(decision, mail));
  return li;
}

/**
 * The correction affordance: up to three ranked labels as one-click chips.
 *
 * Three rather than one is deliberate. Forcing a single guess on personal-email
 * foldering is where accuracy collapses; offering a short ranked list turns a
 * near-miss into one click instead of a hunt through the full taxonomy.
 */
function renderChoices(decision: Decision, mail: MailSummary): HTMLDivElement {
  const wrap = document.createElement('div');
  wrap.className = 'choices';
  const applied = session.applied.get(mail.id);
  const showAll = session.expanded.has(mail.id);

  const ids = showAll
    ? selectableCategories(session.state.taxonomy).map((c) => c.id)
    : decision.ranked
        .map((r) => r.categoryId)
        .filter((id) => id !== NEEDS_REVIEW_ID);

  for (const id of ids) {
    const category = categoryById(session.state.taxonomy, id);
    if (!category) continue;
    const chip = document.createElement('button');
    chip.className = applied === id ? 'choice is-applied' : 'choice';
    chip.textContent = category.name;
    const suggestion = decision.ranked.find((r) => r.categoryId === id);
    if (suggestion) chip.title = `${Math.round(suggestion.confidence * 100)}% confident`;
    chip.addEventListener('click', () => void chooseCategory(mail, id, decision));
    wrap.append(chip);
  }

  if (!showAll) {
    const more = document.createElement('button');
    more.className = 'choice-more';
    more.textContent = 'something else…';
    more.addEventListener('click', () => {
      session.expanded.add(mail.id);
      renderDecisions();
    });
    wrap.append(more);
  }

  return wrap;
}

/**
 * Apply a label the user picked.
 *
 * Anything chosen here counts as a confirmation, which is what earns a sender
 * promotion into a native Outlook rule. Picking the top suggestion is agreement;
 * picking anything else is a correction and is recorded as one.
 */
async function chooseCategory(
  mail: MailSummary,
  categoryId: string,
  decision: Decision,
): Promise<void> {
  try {
    setStatus('Saving…');
    const token = await getGraphToken();
    const suggestion = decision.ranked.find((r) => r.categoryId === categoryId);
    await applyCategories(token, [buildWrite(mail, categoryId, suggestion?.confidence ?? 1)]);

    session.applied.set(mail.id, categoryId);
    session.state.senderRules = upsertSenderRule(
      session.state.senderRules,
      mail.from,
      categoryId,
      true,
    );

    const top = decision.ranked[0];
    const agreed = top?.categoryId === categoryId && !decision.gated;
    session.state.settings.agreementRate = updateAgreement(
      session.state.settings.agreementRate,
      agreed,
    );

    if (!agreed) {
      const correction = {
        sender: mail.from,
        subject: mail.subject,
        fromCategoryId: top?.categoryId ?? null,
        toCategoryId: categoryId,
        at: new Date().toISOString(),
      };
      session.state.recentCorrections.push(correction);
      void appendCorrection(correction);
    }

    session.state = await saveState(session.state);
    renderDecisions();
    setStatus('Saved.');
  } catch (err) {
    handleError(err);
  }
}

// ---------------------------------------------------------------------------
// Automation panel
// ---------------------------------------------------------------------------

function bindAutomation(): void {
  el('promote').addEventListener('click', () => void runPromotion());
}

async function runPromotion(): Promise<void> {
  const button = el('promote') as HTMLButtonElement;
  button.disabled = true;
  try {
    setStatus('Handing senders to Outlook…');
    const token = await getGraphToken();
    const result = await promoteRules(
      token,
      session.state.senderRules,
      session.state.taxonomy,
      session.state.settings.promoteThreshold,
    );
    if (result.promotedPatterns.length > 0) {
      session.state.senderRules = markPromoted(session.state.senderRules, result.promotedPatterns);
      session.state = await saveState(session.state);
    }
    showNote(result.note);
    setStatus(
      result.promotedPatterns.length > 0
        ? `${result.promotedPatterns.length} sender(s) are now sorted automatically on arrival.`
        : 'Nothing new is confident enough yet.',
    );
    await renderAutomation();
  } catch (err) {
    handleError(err);
  } finally {
    button.disabled = false;
  }
}

async function renderAutomation(): Promise<void> {
  const body = el('automation-body');
  body.textContent = 'Loading…';

  try {
    const token = await getGraphToken();
    const owned = await listOwnedRules(token);
    body.textContent = '';

    if (owned.length === 0) {
      const p = document.createElement('p');
      p.className = 'explain';
      p.textContent =
        'Nothing handed off yet. Correct a few labels and senders will start graduating here.';
      body.append(p);
      return;
    }

    for (const rule of owned) {
      const senders = rule.conditions?.senderContains ?? [];
      const group = document.createElement('div');
      group.className = 'automation-group';

      const heading = document.createElement('h3');
      heading.textContent = `${rule.actions?.assignCategories?.[0] ?? rule.displayName} — ${senders.length} sender(s)`;
      group.append(heading);

      const detail = document.createElement('p');
      detail.textContent = senders.slice(0, 8).join(', ') + (senders.length > 8 ? ', …' : '');
      group.append(detail);

      body.append(group);
    }
  } catch (err) {
    body.textContent = '';
    handleError(err);
  }
}

// ---------------------------------------------------------------------------
// Categories panel
// ---------------------------------------------------------------------------

function bindCategories(): void {
  el('add-category').addEventListener('click', () => {
    const id = `custom-${Date.now().toString(36)}`;
    session.state.taxonomy.push({
      id,
      name: 'New category',
      // Cycle through the 25 presets Outlook offers.
      color: `Preset${session.state.taxonomy.length % 25}`,
      description: 'Describe what belongs here. Be specific — this is what the sorter reads.',
    });
    void saveState(session.state).then((s) => {
      session.state = s;
      renderCategories();
    });
  });

  el('sync-categories').addEventListener('click', () => void syncCategories());
}

async function syncCategories(): Promise<void> {
  try {
    setStatus('Creating labels in Outlook…');
    const token = await getGraphToken();
    const result = await ensureMasterCategories(
      token,
      session.state.taxonomy.map((c) => ({ name: c.name, color: c.color })),
    );
    setStatus(
      result.created.length > 0
        ? `Created ${result.created.length} label(s) in Outlook.`
        : 'Outlook already has all of these labels.',
    );
  } catch (err) {
    handleError(err);
  }
}

function renderCategories(): void {
  const list = el('categories');
  list.textContent = '';

  for (const category of session.state.taxonomy) {
    list.append(renderCategoryRow(category));
  }
}

function renderCategoryRow(category: Category): HTMLLIElement {
  const li = document.createElement('li');
  li.className = 'category';
  const isReview = category.id === NEEDS_REVIEW_ID;

  const head = document.createElement('div');
  head.className = 'category-head';

  const name = document.createElement('input');
  name.type = 'text';
  name.value = category.name;
  name.disabled = isReview;
  name.addEventListener('change', () => {
    category.name = name.value.trim() || category.name;
    void saveState(session.state).then((s) => {
      session.state = s;
      setStatus('Renamed. Press "Create these in Outlook" to add the new label.');
    });
  });
  head.append(name);

  const count = document.createElement('span');
  count.className = 'rule-count';
  const learned = session.state.senderRules.filter((r) => r.categoryId === category.id);
  count.textContent = `${learned.length} sender(s)`;
  head.append(count);

  if (!isReview && !SEED_TAXONOMY.some((c) => c.id === category.id)) {
    const drop = document.createElement('button');
    drop.className = 'category-drop';
    drop.textContent = '×';
    drop.title = 'Remove this category';
    drop.addEventListener('click', () => {
      session.state.taxonomy = session.state.taxonomy.filter((c) => c.id !== category.id);
      session.state.senderRules = session.state.senderRules.filter(
        (r) => r.categoryId !== category.id,
      );
      void saveState(session.state).then((s) => {
        session.state = s;
        renderCategories();
      });
    });
    head.append(drop);
  }

  li.append(head);

  if (!isReview) {
    const description = document.createElement('textarea');
    description.value = category.description;
    description.addEventListener('change', () => {
      category.description = description.value;
      void saveState(session.state).then((s) => {
        session.state = s;
        setStatus('Description saved.');
      });
    });
    li.append(description);
  }

  return li;
}

// ---------------------------------------------------------------------------
// Settings panel
// ---------------------------------------------------------------------------

function bindSettings(): void {
  input('api-key').addEventListener('change', () => {
    session.state.settings.geminiApiKey = input('api-key').value.trim();
    void persistSettings('API key saved.');
  });

  select('data-sharing').addEventListener('change', () => {
    session.state.settings.dataSharing = select('data-sharing')
      .value as PersistedState['settings']['dataSharing'];
    renderSharingNote();
    void persistSettings('Sharing preference saved.');
  });

  select('autonomy').addEventListener('change', () => {
    session.state.settings.autonomy = select('autonomy')
      .value as PersistedState['settings']['autonomy'];
    void persistSettings('Saved.');
  });

  input('threshold').addEventListener('input', () => {
    session.state.settings.confidenceThreshold = Number(input('threshold').value);
    el('threshold-out').textContent = formatPercent(session.state.settings.confidenceThreshold);
  });
  input('threshold').addEventListener('change', () => void persistSettings('Saved.'));

  input('promote-threshold').addEventListener('input', () => {
    session.state.settings.promoteThreshold = Number(input('promote-threshold').value);
    el('promote-out').textContent = String(session.state.settings.promoteThreshold);
  });
  input('promote-threshold').addEventListener('change', () => void persistSettings('Saved.'));
}

async function persistSettings(message: string): Promise<void> {
  try {
    session.state = await saveState(session.state);
    setStatus(message);
    renderDiagnostics();
  } catch (err) {
    handleError(err);
  }
}

function renderSettings(): void {
  const s = session.state.settings;
  input('api-key').value = s.geminiApiKey;
  select('data-sharing').value = s.dataSharing;
  select('autonomy').value = s.autonomy;
  input('threshold').value = String(s.confidenceThreshold);
  el('threshold-out').textContent = formatPercent(s.confidenceThreshold);
  input('promote-threshold').value = String(s.promoteThreshold);
  el('promote-out').textContent = String(s.promoteThreshold);
  renderSharingNote();
}

/**
 * Say plainly what each sharing level means.
 *
 * This mailbox holds donor and grant correspondence, and the free Gemini tier
 * states that submitted content may be used to improve Google's products and
 * may be seen by human reviewers. Whoever uses this deserves to read that in
 * the UI rather than discover it later.
 */
function renderSharingNote(): void {
  const note = el('sharing-note');
  switch (session.state.settings.dataSharing) {
    case 'full':
      note.textContent =
        'Best accuracy. Sender, subject, and roughly the first 600 characters go to Google. On the free API tier, Google may use submitted content to improve its products and human reviewers may see it — so message text, including donor and grant details, leaves the mailbox.';
      break;
    case 'metadata':
      note.textContent =
        'Only sender and subject leave the mailbox — never message text. Slightly weaker on unusual mail, and enough for most of it.';
      break;
    case 'rules':
      note.textContent =
        'Nothing leaves the mailbox. Only senders it has already learned get labelled; everything else waits in Needs Review.';
      break;
  }
}

function renderDiagnostics(): void {
  const rules = session.state.senderRules;
  const footprint = stateFootprint(session.state);
  const rows: [string, string][] = [
    ['Senders learned', String(rules.length)],
    ['Handed to Outlook', String(rules.filter((r) => r.promoted).length)],
    ['Agreement rate', formatPercent(session.state.settings.agreementRate)],
    ['Sign-in', describeAuth()],
    [
      'Settings size',
      `${(footprint.bytes / 1024).toFixed(1)} KB of ${(footprint.limit / 1024).toFixed(0)} KB`,
    ],
  ];

  const dl = document.createElement('dl');
  for (const [key, value] of rows) {
    const dt = document.createElement('dt');
    dt.textContent = key;
    const dd = document.createElement('dd');
    dd.textContent = value;
    dl.append(dt, dd);
  }

  const node = el('diagnostics');
  node.textContent = '';
  node.append(dl);

  void signedInAs().then((who) => {
    if (!who) return;
    const p = document.createElement('p');
    p.textContent = `Signed in as ${who}`;
    node.append(p);
  });
}

function formatPercent(n: number): string {
  return `${Math.round(n * 100)}%`;
}

/**
 * Describe the sign-in mechanism. Before the first token request nothing has been
 * negotiated yet, so report what the host supports rather than "unknown".
 */
function describeAuth(): string {
  switch (authMode()) {
    case 'naa':
      return 'single sign-on';
    case 'popup':
      return 'popup sign-in';
    case 'unknown':
      return naaSupported() ? 'single sign-on (not yet used)' : 'popup sign-in (not yet used)';
  }
}
