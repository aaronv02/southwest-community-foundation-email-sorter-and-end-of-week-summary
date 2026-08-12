# How to test this without her mailbox

Four stages, cheapest first. Stage 1 needs nothing. Stages 2–3 need one free
Microsoft account. Stage 4 needs an actual Windows machine.

---

## Stage 1 — Automated (free, already done)

```bash
go test ./...
```

**44 tests.** Two layers:

- **Logic** (`internal/analyze`, `internal/report`) — unanswered-mail detection,
  CC vs To, thread collapsing, grace periods, bulk-sender filtering, week
  windowing across timezones, catch-up after a missed Friday.
- **Graph client** (`internal/graph`) — runs against a stand-in Graph server:
  token caching, pagination across `@odata.nextLink`, 429 with `Retry-After`,
  503 retry, mid-run token revocation, the exact `sendMail` payload, and query
  encoding.

To look at the email itself:

```bash
go test ./internal/report/
open dist/sample-digest.html
```

**What this cannot catch:** whether real Graph responses match the shapes
assumed here, and how Outlook renders the HTML. That needs a real mailbox.

---

## Stage 2 — A real test mailbox (free, ~20 minutes)

> Do **not** hand-write fake emails as JSON. That only re-tests Stage 1. Inject
> them into a real mailbox instead and let the digest read them the way it will
> read hers.

### 2a. Get a tenant

Join the [Microsoft 365 Developer Program](https://developer.microsoft.com/microsoft-365/dev-program) —
free E5 tenant, 25 users, renewable. This matters more than convenience: it
mirrors the director's environment exactly (work M365, you as admin), whereas a
personal outlook.com account differs in both add-in rules and Graph behaviour.
Testing on a personal account risks passing here and failing on delivery.

### 2b. Register a *seeding* app

Separate from the production app. It needs write permissions the real one must
never have:

| Permission | Type | Why |
|---|---|---|
| `Mail.ReadWrite` | Application | inject and delete test mail |
| `Mail.Send` | Application | create genuinely threaded mail |
| `Calendars.ReadWrite` | Application | inject test events |

Grant admin consent, create a secret, then:

```bash
digest --setup     # point it at the DEV mailbox, not hers
digest --check
```

### 2c. Fill the mailbox

```bash
go run ./cmd/seed --inject --i-know-this-is-a-test-mailbox
```

Creates a deliberately awkward week: five items that *should* surface as
waiting, two that should **not** (one inside the grace period, one where she is
only CC'd), four automated senders, three sent messages, and six calendar
entries across two weeks.

Two safety guards, both verified: it refuses without the explicit flag, and it
refuses outright on `director@example.org` even with the flag.

```bash
digest --preview week.html && open week.html
```

**Check the preview against these expectations:**

- [ ] 5 items under "Still waiting on you", Mancos Valley LOI first (oldest)
- [ ] "Quick question about the Hoedown seating" **absent** — inside the 48h grace
- [ ] "FYI - vendor contract copy" **absent** — she was CC'd, not asked
- [ ] Newsletters under "automated", never under waiting
- [ ] Kim Baptiste appears once, not twice, despite being flagged *and* unanswered
- [ ] 4 meetings this week, 2 next week
- [ ] Sent count is 3

Reset any time:

```bash
go run ./cmd/seed --wipe --i-know-this-is-a-test-mailbox
```

### 2d. Test reply detection — the one that matters most

Injected mail cannot thread: `conversationId` is assigned by the service and
can't be set. So the check that stops the digest nagging about mail she already
handled needs **real** mail:

```bash
go run ./cmd/seed --threads --from second.user@yourtenant.onmicrosoft.com \
  --i-know-this-is-a-test-mailbox
```

Then:

1. `digest --preview before.html` → both new items appear as waiting
2. Reply to **one** of them in Outlook
3. `digest --preview after.html` → only the unanswered one remains

If step 3 still shows both, reply detection is broken and the digest would nag
her about handled mail — the fastest way to get it ignored.

---

## Stage 3 — See it in real Outlook

```bash
digest --force
```

Open it in Outlook **on Windows** if at all possible, not just the web. Desktop
Outlook renders mail through Microsoft Word's engine, which drops flexbox,
grid, and external stylesheets. The template is table-based and inline-styled
for that reason, and a test asserts no modern CSS creeps back in — but seeing it
is the only real proof.

Check: headings readable, the three stat boxes sit side by side, nothing
overflows, and it is legible on a phone.

---

## Stage 4 — Windows (the honest gap)

None of this can be verified from a Mac:

| Untested | Risk |
|---|---|
| Scheduled task registration | Medium — standard cmdlets, but unverified |
| Missed-run catch-up firing | **Low** — the week-selection logic behind it has 6 tests |
| DPAPI encrypt/decrypt | Medium — compiles for Windows, never executed |
| SmartScreen flow | Low — documented, just annoying |
| `.cmd` wrappers | Low |

Options, best first:

1. **A Windows machine for 20 minutes** — anyone's. Run
   `Install Weekly Summary.cmd`, then force the task early:
   `Start-ScheduledTask -TaskName "SWCF Weekly Digest"`, and confirm
   `%APPDATA%\SWCFDigest\digest.log` shows a run.
2. **Free Azure Windows VM** (12-month free tier) if the dev tenant already has
   a subscription attached.
3. **Install on the director's machine with her present**, having tested Stages 1–3
   thoroughly. `--check` verifies access before anything is scheduled, and the
   uninstaller is clean, so the blast radius is small.

To confirm catch-up behaviour specifically without waiting a week: disable the
task, let Friday pass, re-enable it on Monday, and confirm the email header says
*"Last week in review"* and covers the correct dates.

---

## Testing the Inbox Sorter

Different project, same tenant.

```bash
cd "../swcommunityfoundation email sorter"
npm test                                   # 34 logic tests, no mailbox
GEMINI_API_KEY=... npm run test:llm        # real classification accuracy
```

The accuracy run scores 50 labelled foundation emails and prints top-1 / top-3
with a confusion list. Targets are **top-1 ≥80%, top-3 ≥90%** — deliberately not
the 95%+ figures quoted elsewhere, which come from spam filtering rather than
topic sorting. A free Gemini key from
[Google AI Studio](https://aistudio.google.com/apikey) takes about two minutes.

If top-1 comes in low, the fix is almost always sharpening the category
descriptions in `src/taxonomy.ts` — those descriptions *are* the model.

Then sideload it against the dev tenant per that project's README and check the
correction loop: mislabel something deliberately, fix the category in Outlook
(not in the panel), re-run, and confirm the sender is right from then on.
