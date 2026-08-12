# Prompts for generating test data

Paste one of these into a fresh Claude conversation. Save the JSON it returns
and feed it straight in — no hand-translation.

Validate before you inject (needs no mailbox, no credentials):

```bash
go run ./cmd/seed --validate-only --file testdata.json
```

Then inject:

```bash
go run ./cmd/seed --file testdata.json --i-know-this-is-a-test-mailbox
```

---

## Prompt 1 — Weekly Digest test data

The digest's logic is what's being tested, not its prose. So this prompt asks
for **adversarial** cases: mail that *looks* like it needs a reply but doesn't,
and vice versa. Pleasant realistic emails prove almost nothing.

> I need test data for a tool that reads an Outlook mailbox and reports which
> emails are still waiting on a reply.
>
> The mailbox belongs to the Executive Director of the Community
> Foundation Serving Southwest Colorado — a small community foundation in
> Durango, Colorado serving five counties (Archuleta, La Plata, Dolores,
> Montezuma, San Juan). It moves about $2.1M a year in grants against $13.6M in
> assets. Her inbox holds donor correspondence, grant applications from local
> nonprofits, scholarship applicants, board governance, event logistics for
> fundraisers (Durango Wine Experience, Hoedown at the Mancos Opera House, 19th
> Hole Concerts, Pitch Palooza), custodian statements, and philanthropy-sector
> newsletters.
>
> The tool flags a message as "waiting on you" only when ALL of these hold:
>  - she is on the To line, not merely CC'd
>  - the sender is a person, not a mailing list or a no-reply robot
>  - it is older than 48 hours
>  - she never replied in that thread
>
> Generate 30 messages as a JSON document in exactly this shape:
>
> ```json
> {
>   "messages": [
>     {
>       "subject": "string",
>       "body": "2-4 sentences, realistic",
>       "fromName": "Firstname Lastname or organization name",
>       "fromAddress": "email@domain.org",
>       "daysAgo": 5.0,
>       "isRead": false,
>       "flagged": false,
>       "sent": false,
>       "ccOnly": false,
>       "expect": "waiting",
>       "why": "one line: why this expectation is correct"
>     }
>   ],
>   "events": [
>     {"subject":"string","daysAgo":1,"startHour":9,"hours":2,"attendees":8,"why":"..."}
>   ]
> }
> ```
>
> `expect` must be exactly one of: `waiting`, `not-waiting`, `unread-person`,
> `unread-bulk`, `sent`.
>
> `daysAgo` counts back from now; `events` count `daysAgo` forward from Monday
> of this week, so 0 = Monday and 8 = next Tuesday.
>
> Required distribution — this is the important part:
>
>  - **8 with `expect: "waiting"`** — genuine unanswered asks, `daysAgo` between
>    3 and 20, spread out. At least one over two weeks old. Vary the pressure:
>    a grant applicant chasing a decision, a lawyer needing the EIN for a
>    bequest, a reporter on deadline, a board member wanting an answer.
>
>  - **8 with `expect: "not-waiting"`**, each defeated by a DIFFERENT rule, and
>    each one written to look urgent so it would fool a naive filter:
>      1. `daysAgo` under 2 — inside the grace period
>      2. `ccOnly: true` — she was copied, not asked
>      3. from a `noreply@` address
>      4. from a `news@` or `newsletter@` address
>      5. from an obvious mailing list with an unsubscribe line in the body
>      6. an automated bank or giving-platform notification
>      7. a calendar/system notification
>      8. `sent: true` — something she sent, which can never be waiting on her
>
>  - **6 with `expect: "unread-bulk"`** — newsletters and automated mail,
>    `isRead: false`. Use realistic senders: Council on Foundations, Colorado
>    Nonprofit Association, Candid, Chronicle of Philanthropy, ColoradoGives,
>    Microsoft 365.
>
>  - **4 with `expect: "unread-person"`** — real people she hasn't opened.
>    Make two of them from the SAME sender so the grouping is exercised.
>
>  - **4 with `expect: "sent"`**, `sent: true`, `daysAgo` between 1 and 5.
>
>  - Mark exactly **one** message `flagged: true`, and make it one of the
>    `waiting` ones — it must appear once, not in two sections.
>
> Also generate 8 events: 5 this week (`daysAgo` 0–4) and 3 next week
> (`daysAgo` 7–11). Include one all-day entry (`"allDay": true`) and one long
> board meeting with 9+ attendees.
>
> Use plausible Southwest Colorado organizations and people. Do not reuse the
> same domain for unrelated senders. Output only the JSON, no commentary.

---

## Prompt 2 — Inbox Sorter accuracy fixtures

Different goal: this measures classification accuracy, so the value is in
**boundary cases**. Emails that obviously belong somewhere prove nothing —
accuracy dies on the ones that could credibly go two ways.

> I need test fixtures for an email classifier that sorts a community
> foundation executive director's mail into categories.
>
> The organization is the Community Foundation Serving Southwest Colorado in
> Durango. Programs include donor advised funds, scholarships, the LAUNCH Fund,
> the Community Emergency Relief Fund, fiscal sponsorship, and fundraising
> events (Durango Wine Experience, Hoedown at the Mancos Opera House, 19th Hole
> Concerts, Pitch Palooza, Making a Difference Speaker Series).
>
> The categories, with the distinctions that actually matter:
>
> | id | what belongs, and what does not |
> |---|---|
> | `donors` | Individual donors and their gifts, fund inquiries, planned giving. NOT automated statements about gift money — those are `finance`. |
> | `grants` | Organizations seeking or reporting on money: LOIs, applications, grant reports, declines. NOT the same org asking about a workshop — that is `partners`. |
> | `scholarships` | Student applicants, review committees, disbursements, schools. |
> | `board` | Governance: agendas, minutes, policies, board recruitment. A board member writing as a donor is `donors`. |
> | `finance` | Statements, payouts, invoices, bookkeeping, audit, 990. |
> | `events` | Fundraiser logistics, sponsors, venues, caterers, volunteers. Sponsorship solicitation is `events`, not `donors`. |
> | `partners` | Nonprofits in a NON-funding capacity: fiscal sponsorship, training, capacity building, collaboration. |
> | `press` | Media requests, interviews, awards, speaking invitations, community announcements. |
> | `sector` | Bulk philanthropy-sector newsletters requiring no action. |
> | `vendors` | Software, IT, subscriptions, office, facilities. |
>
> Generate 40 emails as a JSON array in exactly this shape:
>
> ```json
> [
>   {
>     "from": "email@domain.org",
>     "fromName": "Sender Name",
>     "subject": "string",
>     "preview": "first 2-3 sentences of the body, realistic",
>     "expected": "grants",
>     "hasAttachments": false
>   }
> ]
> ```
>
> Composition:
>
>  - 4 per category for all 10 categories.
>  - **At least 12 must be genuine boundary cases** — plausibly two categories,
>    where the correct answer depends on the distinctions in the table above.
>    Examples of the kind I want: a nonprofit that received a grant last year now
>    asking about a training workshop (`partners`, not `grants`); a board member
>    asking about her own donor advised fund (`donors`, not `board`); a business
>    offering event sponsorship (`events`, not `donors`); a giving-platform
>    payout report that names individual donors (`finance`, not `donors`); a
>    scholarship donor asking who received the award (`scholarships`, not
>    `donors`).
>  - Vary the register: some formal and long, some two-line notes, some
>    forwarded chains, some clearly automated.
>  - Use plausible Southwest Colorado organizations, schools, and people. Do not
>    reuse a domain across unrelated senders.
>
> Output only the JSON array, no commentary.

Save that as `test/fixtures/emails.json` in the sorter project, then:

```bash
npm test                                # checks every fixture names a real category
GEMINI_API_KEY=... npm run test:llm     # scores top-1 / top-3 accuracy
```

---

## A caution about generated test data

Generated fixtures share the blind spots of the thing that generated them. If
the classifier and the test data are both produced by an LLM reasoning from the
same category descriptions, high accuracy partly measures agreement rather than
correctness.

That is fine for catching regressions and obvious breakage, which is what this
is for. It is not proof it will work on the director's actual mail. The only real
proof is running it against a real mailbox and having her look at the output —
which is why the digest ships in suggest-only mode and the sorter labels
low-confidence mail "Needs Review" rather than guessing.
