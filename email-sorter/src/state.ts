import type { Correction, PersistedState, SenderRule } from './types.js';
import { defaultState, STATE_VERSION } from './taxonomy.js';

/**
 * Persistence, in two tiers.
 *
 * Tier 1 - Office roamingSettings: authoritative, server-side, follows the
 * mailbox to every device and survives reinstalls. Its size limit is genuinely
 * unclear: the API reference documents 2 MB total / 32 KB per setting while the
 * Outlook limits page says 32 KB total, and there are open office-js issues
 * reporting that neither figure is what's actually enforced. We therefore treat
 * 30 KB as a hard ceiling for the whole payload, store a compacted form, and
 * evict the least valuable sender rules rather than ever risking a failed save.
 *
 * Tier 2 - IndexedDB: the full correction corpus and per-message provenance.
 * Large, but per-client and clearable, so nothing here may be authoritative.
 */

const ROAMING_KEY = 'inboxSteward.state.v1';
const SAFE_BYTES = 30_000;

/** Hard caps, enforced before every save. */
const MAX_SENDER_RULES = 400;
const MAX_RECENT_CORRECTIONS = 25;

// ---------------------------------------------------------------------------
// Compact wire form
// ---------------------------------------------------------------------------

/**
 * roamingSettings is the tightest budget in the system, so state is stored with
 * single-character keys and positional arrays. This roughly halves the payload
 * versus pretty JSON, which is the difference between ~200 sender rules and ~400.
 */
interface WireState {
  v: number;
  s: [string, string, string, string, number, number, number, 0 | 1];
  t: [string, string, string, string][];
  r: [string, string, number, number, 0 | 1, string][];
  c: [string, string, string | null, string, string][];
}

function encode(state: PersistedState): string {
  const { settings: g } = state;
  const wire: WireState = {
    v: state.version,
    s: [
      g.geminiApiKey,
      g.model,
      g.autonomy,
      g.dataSharing,
      g.confidenceThreshold,
      g.promoteThreshold,
      g.agreementRate,
      g.bootstrapped ? 1 : 0,
    ],
    t: state.taxonomy.map((c) => [c.id, c.name, c.color, c.description]),
    r: state.senderRules.map((r) => [
      r.pattern,
      r.categoryId,
      r.hits,
      r.confirmations,
      r.promoted ? 1 : 0,
      r.createdAt,
    ]),
    c: state.recentCorrections.map((c) => [
      c.sender,
      c.subject,
      c.fromCategoryId,
      c.toCategoryId,
      c.at,
    ]),
  };
  return JSON.stringify(wire);
}

function decode(raw: string): PersistedState {
  const wire = JSON.parse(raw) as WireState;
  const [key, model, autonomy, sharing, conf, promote, agree, boot] = wire.s;
  return {
    version: wire.v,
    settings: {
      geminiApiKey: key,
      model,
      autonomy: autonomy as PersistedState['settings']['autonomy'],
      dataSharing: sharing as PersistedState['settings']['dataSharing'],
      confidenceThreshold: conf,
      promoteThreshold: promote,
      agreementRate: agree,
      bootstrapped: boot === 1,
    },
    taxonomy: wire.t.map(([id, name, color, description]) => ({ id, name, color, description })),
    senderRules: wire.r.map(([pattern, categoryId, hits, confirmations, promoted, createdAt]) => ({
      pattern,
      categoryId,
      hits,
      confirmations,
      promoted: promoted === 1,
      createdAt,
    })),
    recentCorrections: wire.c.map(([sender, subject, fromCategoryId, toCategoryId, at]) => ({
      sender,
      subject,
      fromCategoryId,
      toCategoryId,
      at,
    })),
  };
}

// ---------------------------------------------------------------------------
// Trimming
// ---------------------------------------------------------------------------

/**
 * Rank sender rules by how much we'd regret losing them. A rule the user
 * explicitly corrected into place is worth far more than one we inferred and
 * applied once, so confirmations dominate hits.
 */
function ruleValue(r: SenderRule): number {
  return r.confirmations * 10 + Math.min(r.hits, 50);
}

/**
 * Bring state within the roaming budget. Returns the encoded payload.
 *
 * Order of sacrifice: oldest corrections first (they also live in IndexedDB, so
 * losing them here only weakens few-shot priming on a fresh device), then the
 * least valuable sender rules. Taxonomy and settings are never trimmed.
 */
export function encodeWithinBudget(state: PersistedState): {
  payload: string;
  trimmed: PersistedState;
} {
  const trimmed: PersistedState = {
    ...state,
    senderRules: [...state.senderRules]
      .sort((a, b) => ruleValue(b) - ruleValue(a))
      .slice(0, MAX_SENDER_RULES),
    recentCorrections: state.recentCorrections.slice(-MAX_RECENT_CORRECTIONS),
  };

  let payload = encode(trimmed);
  while (byteLength(payload) > SAFE_BYTES) {
    if (trimmed.recentCorrections.length > 5) {
      trimmed.recentCorrections = trimmed.recentCorrections.slice(1);
    } else if (trimmed.senderRules.length > 1) {
      trimmed.senderRules = trimmed.senderRules.slice(0, -1);
    } else {
      // Only settings and taxonomy remain. If that alone exceeds the budget the
      // user has written enormous category descriptions; let the save attempt
      // proceed and surface the real error rather than looping forever.
      break;
    }
    payload = encode(trimmed);
  }
  return { payload, trimmed };
}

function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

// ---------------------------------------------------------------------------
// roamingSettings access
// ---------------------------------------------------------------------------

export function loadState(): PersistedState {
  try {
    const raw = Office.context.roamingSettings.get(ROAMING_KEY) as string | undefined;
    if (!raw) return defaultState();
    const parsed = decode(raw);
    if (parsed.version !== STATE_VERSION) return migrate(parsed);
    return parsed;
  } catch (err) {
    console.warn('[state] could not read roaming settings, starting fresh', err);
    return defaultState();
  }
}

/**
 * Future schema changes land here. Today there is only v1, so anything else is
 * treated as unreadable and replaced rather than half-interpreted.
 */
function migrate(old: PersistedState): PersistedState {
  const fresh = defaultState();
  if (old.settings) fresh.settings = { ...fresh.settings, ...old.settings };
  return fresh;
}

export async function saveState(state: PersistedState): Promise<PersistedState> {
  const { payload, trimmed } = encodeWithinBudget(state);
  Office.context.roamingSettings.set(ROAMING_KEY, payload);
  await new Promise<void>((resolve, reject) => {
    Office.context.roamingSettings.saveAsync((result) => {
      if (result.status === Office.AsyncResultStatus.Succeeded) resolve();
      else reject(result.error);
    });
  });
  return trimmed;
}

/** Diagnostic for the settings pane, so the budget is never a surprise. */
export function stateFootprint(state: PersistedState): { bytes: number; limit: number } {
  return { bytes: byteLength(encode(state)), limit: SAFE_BYTES };
}

// ---------------------------------------------------------------------------
// IndexedDB: full correction corpus
// ---------------------------------------------------------------------------

const DB_NAME = 'inbox-steward';
const DB_VERSION = 1;
const STORE_CORRECTIONS = 'corrections';

function openDb(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE_CORRECTIONS)) {
        db.createObjectStore(STORE_CORRECTIONS, { keyPath: 'seq', autoIncrement: true });
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

export async function appendCorrection(c: Correction): Promise<void> {
  try {
    const db = await openDb();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE_CORRECTIONS, 'readwrite');
      tx.objectStore(STORE_CORRECTIONS).add(c);
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
    db.close();
  } catch (err) {
    // The corpus is an optimization, not a source of truth - roamingSettings
    // already holds the recent window and the sender rule. Never fail a sort
    // because local storage misbehaved.
    console.warn('[state] could not append correction to IndexedDB', err);
  }
}

/** Most recent corrections first, for few-shot priming. */
export async function recentCorpus(limit: number): Promise<Correction[]> {
  try {
    const db = await openDb();
    const all = await new Promise<Correction[]>((resolve, reject) => {
      const tx = db.transaction(STORE_CORRECTIONS, 'readonly');
      const req = tx.objectStore(STORE_CORRECTIONS).getAll();
      req.onsuccess = () => resolve(req.result as Correction[]);
      req.onerror = () => reject(req.error);
    });
    db.close();
    return all.slice(-limit).reverse();
  } catch (err) {
    console.warn('[state] could not read correction corpus', err);
    return [];
  }
}
