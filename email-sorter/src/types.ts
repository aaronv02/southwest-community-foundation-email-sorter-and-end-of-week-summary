/** Shared types. Kept dependency-free so the test harness can import it in Node. */

export interface Category {
  /** Stable identifier. Survives renames, so sender rules never break. */
  id: string;
  /** Display name. This is the literal Outlook category name. Must be unique. */
  name: string;
  /** One of Preset0..Preset24 - Outlook offers exactly 25 preset colors. */
  color: string;
  /** Fed to the LLM verbatim. The quality of these descriptions is most of the accuracy. */
  description: string;
}

/** A message reduced to the fields we classify on. */
export interface MailSummary {
  id: string;
  from: string;
  fromName: string;
  subject: string;
  received: string;
  hasAttachments: boolean;
  /** Body preview. Omitted entirely when dataSharing is 'metadata' or 'rules'. */
  preview?: string;
  /** Present when the message advertises itself as a mailing list. */
  listId?: string;
  /** Categories currently on the message, as Outlook reports them. */
  categories: string[];
  /**
   * What we previously assigned, read back from the provenance stamp. Present
   * only on messages this add-in has already labelled. Comparing it against
   * `categories` is how corrections made directly in Outlook are detected.
   */
  assigned?: { categoryId: string; confidence: number; at: string };
}

/** One ranked guess. */
export interface Suggestion {
  categoryId: string;
  confidence: number;
  reason: string;
}

/** The classifier's verdict for one message. */
export interface Decision {
  messageId: string;
  /** Ranked best-first. We show the top 3; benchmarks say that is where the accuracy is. */
  ranked: Suggestion[];
  /** Which layer produced this. Drives both UI copy and quota accounting. */
  source: 'rule' | 'llm' | 'unresolved';
  /** True when confidence fell below threshold and we labelled Needs Review instead. */
  gated: boolean;
}

/**
 * A learned sender -> category mapping. `pattern` is either a full address
 * ("board@example.org") or a domain ("@coloradogives.org").
 */
export interface SenderRule {
  pattern: string;
  categoryId: string;
  /** How many messages we have applied this to. Drives promotion. */
  hits: number;
  /** How many times the user explicitly corrected mail INTO this category. */
  confirmations: number;
  /** True once a native Outlook rule covers this sender. */
  promoted: boolean;
  createdAt: string;
}

/** A user correction. These become few-shot examples and new sender rules. */
export interface Correction {
  sender: string;
  subject: string;
  /** What we guessed. Null when we had no guess. */
  fromCategoryId: string | null;
  /** What the user actually wanted. */
  toCategoryId: string;
  at: string;
}

export type Autonomy = 'suggest' | 'graduated' | 'auto';

/**
 * How much of each message may leave the mailbox.
 *  - 'full'     : subject, sender, and body preview go to the LLM.
 *  - 'metadata' : subject and sender only. No body, ever.
 *  - 'rules'    : nothing leaves. Sender rules only, no LLM calls at all.
 */
export type DataSharing = 'full' | 'metadata' | 'rules';

export interface Settings {
  geminiApiKey: string;
  model: string;
  autonomy: Autonomy;
  dataSharing: DataSharing;
  /** Below this, a message gets Needs Review rather than a confident wrong label. */
  confidenceThreshold: number;
  /** Confirmations needed on a sender before it earns a native Outlook rule. */
  promoteThreshold: number;
  /** Rolling agreement rate, used by 'graduated' autonomy to decide when to go auto. */
  agreementRate: number;
  /** Set once the first run has created the master category list. */
  bootstrapped: boolean;
}

/** Everything that must survive across devices. Budgeted to stay under 32 KB. */
export interface PersistedState {
  version: number;
  settings: Settings;
  taxonomy: Category[];
  senderRules: SenderRule[];
  /** Only the most recent few - the full corpus lives in IndexedDB. */
  recentCorrections: Correction[];
}

/** Minimal shape of what Graph returns for a message. */
export interface GraphMessage {
  id: string;
  subject: string | null;
  bodyPreview: string | null;
  receivedDateTime: string;
  hasAttachments: boolean;
  categories: string[] | null;
  from: { emailAddress: { address: string; name: string } } | null;
  internetMessageHeaders?: { name: string; value: string }[];
  singleValueExtendedProperties?: { id: string; value: string }[];
}
