# Inbox Steward

An Outlook add-in that labels mail with Outlook **categories**, learns from corrections, and then teaches Outlook's own rules to keep doing it without the add-in running.

Built for the Executive Director of a community foundation, so the default categories are grants, donors, scholarships, board, events, and so on. All of it is editable in the UI.

Nothing moves and nothing is deleted. Categories are colored tags, so every action is visible and reversible.

---

## How it works

**Three layers, cheapest first.** Layer order is the whole economics of the tool.

1. **Sender rules** — free, instant, deterministic. `sender@example.org → Grants`. Grown from every correction. Handles the repetitive majority with zero API calls.
2. **The LLM** — only for senders no rule covers. Batched ~20 messages per request to conserve free-tier quota. The prompt carries the category descriptions plus recent corrections as few-shot examples, which is the entire learning mechanism — no retraining, no embeddings.
3. **A confidence gate** — anything that doesn't clear the bar, or that is nearly tied between two categories, gets `⚠ Needs Review` instead of a confident wrong guess.

**Then promotion, which is the important part.** Outlook add-ins have **no "message received" event** — the whole event surface is compose/send/read oriented. An add-in can therefore only sort while it is open. So once a sender has been confirmed a few times, Inbox Steward writes it into a **native Outlook rule**. Native rules run server-side on delivery, sync to every client, and cost nothing. From then on that sender is categorized the moment mail arrives, whether or not anyone opens the taskpane again.

The smart layer teaches the always-on layer and gradually works itself out of a job.

Only **user-confirmed** senders are promoted. A native rule keeps applying itself invisibly, so baking in a guess would compound quietly.

**Corrections need no new habit.** Change a category in Outlook the normal way. We stamp what we assigned onto each message as a hidden MAPI property, so on the next run any divergence is detected as a correction. That writes a sender rule, feeds the few-shot set, and counts toward promotion.

---

## Cost

Free, in the ordinary case.

| Piece | Cost |
|---|---|
| Static hosting (Cloudflare Pages) | $0 |
| Entra app registration | $0 |
| Microsoft Graph mail API | $0 — no per-call charge |
| Outlook rules | $0 |
| Gemini API | $0 on the free tier |

There is no backend, so there is nothing to run. The only ceiling is the Gemini free-tier daily request cap, and layer 1 is designed so that steady-state usage drifts toward zero AI calls.

---

## Setup

### 0. Prerequisites

- Node 20+
- A **work or school Microsoft 365 account** whose tenant you can administer.
- A free Gemini API key from [Google AI Studio](https://aistudio.google.com/apikey).

> **Personal outlook.com accounts probably will not work.** Official docs say add-ins *run* on consumer Outlook.com, but sideloading a *custom* manifest appears to require an Exchange Online mailbox — multiple reports have the "Add a custom add-in" option simply absent, and `aka.ms/olksideload` resolves to the work/school host. Test on a work tenant. A free [Microsoft 365 Developer Program](https://developer.microsoft.com/microsoft-365/dev-program) tenant (E5, 25 users, renewable) is the right dev environment because it matches the real target exactly.

### 1. Install

```bash
npm install
```

### 2. Register the Entra application

This is what lets the taskpane get a Microsoft Graph token with no server.

1. Go to the [Entra portal → App registrations](https://entra.microsoft.com) → **New registration**.
2. Name it anything. For **Supported account types** choose **Accounts in any organizational directory and personal Microsoft accounts**.
3. Skip the redirect URI on this screen. Register.
4. Open **Authentication** → **Add a platform** → **Single-page application**, and add exactly:

   ```
   brk-multihub://your-domain.pages.dev
   ```

   Origin only — **no path, no trailing slash**. This is Nested App Authentication's required redirect form.
5. Under **API permissions**, add delegated Microsoft Graph permissions: `Mail.ReadWrite`, `MailboxSettings.ReadWrite`, `User.Read`. None require admin consent.
6. Copy the **Application (client) ID**.

> The `brk-multihub://` redirect authorizes an entire origin. Use a dedicated domain, **not** a shared `*.github.io` subdomain, where it would cover every project you host there.

### 3. Configure

```bash
cp .env.example .env
```

Put your client ID in `.env`:

```
VITE_ENTRA_CLIENT_ID=00000000-0000-0000-0000-000000000000
```

### 4. Deploy

```bash
npm run build
npx wrangler pages deploy dist --project-name inbox-steward
```

Any static host with HTTPS works — Cloudflare Pages, Netlify, Vercel, GitHub Pages. Every URL in the manifest must be HTTPS; plain HTTP fails to load.

### 5. Point the manifest at your deployment

Replace every `https://inbox-steward.pages.dev` in `manifest.xml` with your own origin:

```bash
sed -i '' 's|https://inbox-steward.pages.dev|https://your-domain.pages.dev|g' manifest.xml
```

### 6. Sideload

1. Open <https://aka.ms/olksideload>. Outlook on the web opens with the add-ins dialog.
2. **My add-ins** → scroll to **Custom Addins** → **Add a custom add-in** → **Add from File**.
3. Pick your edited `manifest.xml` and accept the prompts.

It then appears in every Outlook client signed in to that mailbox. Classic Outlook on Windows can take up to 24 hours to notice, due to caching.

On **new Outlook for Mac (16.85+)** the *Get Add-ins* button no longer opens this dialog — sideload via Outlook on the web and let it propagate.

### 7. First run

Open the add-in, go to **Settings**, paste your Gemini key, then press **Sort my inbox**. The first run creates the category list in the mailbox and quietly mines any mail you have already categorized as training data.

---

## Development

You do not need a mailbox to work on this.

```bash
npm run dev
```

`src/dev-mock.ts` stands in for both Office.js and Graph, backed by the 50 fixture emails in `test/fixtures/`. The full flow works in an ordinary browser: sorting, gating, correcting, learning, and rule promotion (created rules are logged to the console). Paste a real Gemini key into Settings and layer 2 genuinely runs.

The mock is excluded from production builds — `import.meta.env.DEV` is statically false, so it and its fixtures are tree-shaken out.

```bash
npm test          # 34 logic checks, no network, no mailbox
npm run test:llm  # additionally measures real accuracy; needs GEMINI_API_KEY
npm run typecheck
```

`npm run test:llm` reports top-1 and top-3 accuracy against the fixtures and lists every miss as `expected -> predicted`, which is the fastest way to tell whether a category description needs sharpening.

**Expected accuracy:** roughly **80% top-1, 90% top-3**. Personal-email foldering is genuinely harder than it sounds — the published benchmarks land around 60–80% for a single forced guess, and 80–98% when three options are offered. That is exactly why the UI shows three ranked chips instead of one answer. Ignore the 95%+ figures you'll see quoted elsewhere; those come from spam/ham and news-topic corpora, not personal folder taxonomies.

### Layout

```
manifest.xml              XML add-in manifest (not the unified JSON one - that
                          isn't supported on Mac or mobile)
src/
  taskpane.html/.ts       UI shell and controller
  auth.ts                 NAA/MSAL -> Graph token, feature-detected
  graph.ts                messages, categories, master list, message rules
  engine.ts               layer orchestration, correction detection
  promote.ts              sender rules -> native Outlook rules
  classify/
    rules.ts              layer 1
    llm.ts                layer 2 (Gemini native REST)
    confidence.ts         layer 3
  state.ts                roamingSettings + IndexedDB
  taxonomy.ts             seed categories
  dev-mock.ts             dev-only Office.js + Graph stand-in
test/run.ts               offline harness
scripts/make-icons.mjs    regenerates the manifest icons
```

### Where state lives

| Store | Holds | Limit |
|---|---|---|
| `roamingSettings` | settings, taxonomy, sender rules | **treated as 32 KB.** The docs contradict themselves (2 MB in the API reference vs 32 KB in the Outlook limits page) and open office-js issues report neither is enforced as written. State is stored in a compacted form and the least valuable sender rules are evicted before a save can fail. |
| MAPI named property | our verdict per message | how corrections are detected |
| IndexedDB | full correction corpus | per-device, not authoritative |
| The categories themselves | ground-truth labels | free, and they roam |

---

## Tuning it

Almost all accuracy lives in the **category descriptions** in the Categories tab (defaults in `src/taxonomy.ts`). They are written for an LLM reader: concrete nouns, real program names, and explicit disambiguation against neighbouring categories — for example, spelling out that a nonprofit asking for money is *Grants* while the same nonprofit asking about a workshop is *Nonprofit Partners*. When something is consistently misfiled, sharpen the description rather than touching the code.

**Settings worth knowing:**

- **Autonomy** — `suggest` (default) proposes and waits; `graduated` starts by asking and switches to automatic once the rolling agreement rate passes 85%; `auto` labels everything immediately.
- **Confidence threshold** — lower labels more mail, higher sends more to Needs Review.
- **Corrections before handoff** — how many confirmations a sender needs before it earns a native Outlook rule. Higher is more conservative.

---

## Data handling

This is configured for **full content**: sender, subject, and roughly the first 600 characters of each message from unrecognized senders go to the Gemini API.

**On the free tier, Google's terms state submitted content may be used to improve its products, and that human reviewers may see it.** For a foundation mailbox that means donor names, gift amounts, and grant deliberations leave the mailbox. The Settings pane says so plainly, and the level is one dropdown:

- **Full content** — best accuracy. Message text leaves the mailbox.
- **Subject and sender only** — no body text, ever. Slightly weaker on unusual mail.
- **Nothing leaves** — learned senders only; everything else waits in Needs Review.

Switching to a **paid** Gemini tier changes the terms so content is not used for training, and costs a few dollars a month at this volume. Users in the EEA, Switzerland, or the UK get the paid data terms on all tiers.

The API key is stored in mailbox roaming settings and sent as an `x-goog-api-key` header, never in a URL. Restrict it by HTTP referrer in the Google Cloud console.

---

## Known limits

- **No sorting on arrival until a sender is promoted.** There is no receive event for add-ins; that gap is exactly what rule promotion closes. New and one-off senders still need the taskpane opened.
- **Native rule quota.** Exchange allows ~256 KB of rule data per mailbox. Senders are consolidated at up to 60 per rule and we stop at 160 KB to leave the user room; the UI says so when it stops.
- **Gemini free-tier limits are volatile.** Google no longer publishes a stable per-model table, so nothing is hardcoded — a 429 degrades to rules-only rather than failing.
- **Mac has fewer capabilities.** New Outlook for Mac tops out at requirement set 1.14 and classic Mac at 1.8, so `loadItemByIdAsync` (1.15) is unavailable there. Outlook on the web is the primary target.
- **Delegates can't manage the master category list**, so the category-creation step needs the mailbox owner.
- **December 31, 2026:** a new admin-consent `Mail-Advanced.ReadWrite` permission will gate modifying *sensitive* message properties. The evidence says it covers subject/body/recipients and **not** `categories`, but that came from a secondary source — worth re-checking before that date.
