/**
 * Development harness. Loaded only by `npm run dev`, never in a production build.
 *
 * Outlook add-ins are otherwise untestable without a mailbox: Office.js only
 * exists inside Outlook, and Graph needs a real tenant. This stands in for both
 * so the entire flow - sorting, gating, correcting, learning, promoting - can be
 * exercised in an ordinary browser against the test fixtures.
 *
 * Set a real Gemini key in Settings and layer 2 will genuinely run; leave it
 * blank and you see the rules-only path.
 */

import fixtures from '../test/fixtures/emails.json';
import { PROVENANCE_PROP_ID } from './graph.js';

interface Fixture {
  from: string;
  fromName: string;
  subject: string;
  preview: string;
  expected: string;
  hasAttachments?: boolean;
  listId?: string;
}

const DEV_TOKEN = 'dev-mock-token';

// ---------------------------------------------------------------------------
// Fake mailbox
// ---------------------------------------------------------------------------

interface FakeMessage {
  id: string;
  subject: string;
  bodyPreview: string;
  receivedDateTime: string;
  hasAttachments: boolean;
  categories: string[];
  from: { emailAddress: { address: string; name: string } };
  provenance?: string;
}

const messages: FakeMessage[] = (fixtures as Fixture[]).map((f, i) => ({
  id: `dev-${i}`,
  subject: f.subject,
  bodyPreview: f.preview,
  receivedDateTime: new Date(Date.UTC(2026, 6, 1, 8, i)).toISOString(),
  hasAttachments: f.hasAttachments ?? false,
  categories: [],
  from: { emailAddress: { address: f.from, name: f.fromName } },
}));

const masterCategories: { id: string; displayName: string; color: string }[] = [];
let messageRules: Record<string, unknown>[] = [];
let ruleSeq = 1;

function serialize(m: FakeMessage) {
  return {
    id: m.id,
    subject: m.subject,
    bodyPreview: m.bodyPreview,
    receivedDateTime: m.receivedDateTime,
    hasAttachments: m.hasAttachments,
    categories: m.categories,
    from: m.from,
    ...(m.provenance
      ? { singleValueExtendedProperties: [{ id: PROVENANCE_PROP_ID, value: m.provenance }] }
      : {}),
  };
}

// ---------------------------------------------------------------------------
// Office.js stand-in
// ---------------------------------------------------------------------------

const ROAMING_STORE = 'dev-roaming-settings';

function readRoaming(): Record<string, unknown> {
  try {
    return JSON.parse(localStorage.getItem(ROAMING_STORE) ?? '{}') as Record<string, unknown>;
  } catch {
    return {};
  }
}

const roaming = readRoaming();

const officeMock = {
  onReady(callback: (info: { host: string; platform: string }) => void) {
    // Async, matching the real thing, so ordering bugs surface here too.
    setTimeout(() => callback({ host: 'Outlook', platform: 'OfficeOnline' }), 0);
  },
  HostType: { Outlook: 'Outlook' },
  AsyncResultStatus: { Succeeded: 'succeeded', Failed: 'failed' },
  context: {
    roamingSettings: {
      get: (key: string) => roaming[key],
      set: (key: string, value: unknown) => {
        roaming[key] = value;
      },
      remove: (key: string) => {
        delete roaming[key];
      },
      saveAsync: (callback: (result: { status: string }) => void) => {
        localStorage.setItem(ROAMING_STORE, JSON.stringify(roaming));
        setTimeout(() => callback({ status: 'succeeded' }), 0);
      },
    },
    requirements: {
      // Report NAA as available so the primary auth path is the one exercised.
      isSetSupported: (name: string) => name === 'NestedAppAuth' || name === 'Mailbox',
    },
  },
};

(globalThis as Record<string, unknown>).Office = officeMock;
(globalThis as Record<string, unknown>).__INBOX_STEWARD_DEV_TOKEN__ = DEV_TOKEN;

// ---------------------------------------------------------------------------
// Graph stand-in
// ---------------------------------------------------------------------------

const realFetch = globalThis.fetch.bind(globalThis);

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

globalThis.fetch = async (inputUrl: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = typeof inputUrl === 'string' ? inputUrl : inputUrl.toString();

  // Anything that isn't Graph - notably the real Gemini endpoint - passes through.
  if (!url.includes('graph.microsoft.com')) return realFetch(inputUrl, init);

  const method = (init?.method ?? 'GET').toUpperCase();
  const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null;

  if (url.includes('/$batch') && body) {
    const requests = body.requests as {
      id: string;
      url: string;
      body: { categories?: string[]; singleValueExtendedProperties?: { value: string }[] };
    }[];
    const responses = requests.map((req) => {
      const id = req.url.split('/me/messages/')[1] ?? '';
      const message = messages.find((m) => m.id === id);
      if (!message) return { id: req.id, status: 404 };
      if (req.body.categories) message.categories = req.body.categories;
      const stamp = req.body.singleValueExtendedProperties?.[0]?.value;
      if (stamp) message.provenance = stamp;
      return { id: req.id, status: 200 };
    });
    return json({ responses });
  }

  if (url.includes('/outlook/masterCategories')) {
    if (method === 'POST' && body) {
      masterCategories.push({
        id: `cat-${masterCategories.length}`,
        displayName: String(body.displayName),
        color: String(body.color),
      });
      return json(masterCategories.at(-1));
    }
    return json({ value: masterCategories });
  }

  if (url.includes('/messageRules')) {
    if (method === 'POST' && body) {
      const rule = { id: `rule-${ruleSeq++}`, ...body };
      messageRules.push(rule);
      console.info('[dev-mock] created Outlook rule', rule);
      return json(rule);
    }
    if (method === 'PATCH' && body) {
      const id = url.split('/messageRules/')[1] ?? '';
      messageRules = messageRules.map((r) => (r.id === id ? { ...r, ...body } : r));
      return json({});
    }
    if (method === 'DELETE') return new Response(null, { status: 204 });
    return json({ value: messageRules });
  }

  if (url.includes('/mailFolders/inbox/messages')) {
    return json({ value: messages.map(serialize) });
  }

  if (url.includes('/mailFolders')) {
    return json({ value: [{ id: 'inbox', displayName: 'Inbox' }] });
  }

  console.warn('[dev-mock] unhandled Graph call', method, url);
  return json({ value: [] });
};

console.info(
  `[dev-mock] ${messages.length} fixture messages loaded. ` +
    'Add a Gemini key in Settings to exercise the AI layer.',
);
