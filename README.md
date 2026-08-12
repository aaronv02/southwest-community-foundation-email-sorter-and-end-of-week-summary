# Outlook email sorter and end-of-week summary

Two tools for a small nonprofit's Outlook mailbox, built for the
[Community Foundation Serving Southwest Colorado](https://www.swcommunityfoundation.org/)
and generic enough for any small organization where one person's inbox is the
bottleneck.

- **Email sorter** — labels mail with colored categories, learns from
  corrections, and teaches Outlook's own rules to keep doing it
- **End-of-week summary** — a Friday email covering what is still waiting on a
  reply, what never got opened, what the week looked like, and what is coming

Both read one mailbox through the Microsoft Graph API. Nothing is moved or
deleted; the only change either makes is adding a category, which is reversible
in one click.

---

## The constraint that shaped everything

**Outlook add-ins have no "message received" event.** The entire event surface
is compose/send/read oriented — nothing fires on delivery. So an add-in can only
sort while it is open, and no amount of cleverness changes that.

The workaround is the most interesting idea here: once the sorter is confident
about a sender, it writes that knowledge into a **native Outlook rule**. Those
run server-side on delivery, sync to every device, cost nothing, and keep
working with no app open and no computer on.

The smart-but-sleepy layer trains the dumb-but-always-awake one, and gradually
works itself out of a job.

---

## What's in here

| Folder | What | Language |
|---|---|---|
| [`email-sorter/`](email-sorter/) | Outlook add-in: taskpane, three-layer classifier, rule promotion | TypeScript |
| [`weekly-summary/`](weekly-summary/) | Friday digest, MCP server for Claude Desktop, test-mailbox seeder | Go |
| [`package-for-usb/`](package-for-usb/) | Builds a drop-in folder for a non-technical user | Bash |

### Three ways to use it

Because the add-in is the piece most likely to be blocked by tenant policy,
there is more than one route to the same result:

| | Setup | Sorts new senders | Runs unattended |
|---|---|---|---|
| **Outlook add-in** | needs hosting + sideloading | yes | only via promoted rules |
| **Claude Desktop** (`weekly-summary --mcp`) | one config file | yes, best quality | no, on demand |
| **Friday email** | scheduled task | n/a | yes |

---

## Design decisions worth reading

**The waiting list is deliberately conservative.** A message counts as
"waiting on you" only if she was on the To line (not CC'd), the sender looks
human, it is past a grace period, and no reply went out in that thread. A nag
list containing newsletters gets ignored within two weeks, so every filter
exists to protect precision at the cost of recall.

**Uncertainty is labelled, not guessed.** Below a confidence threshold, or when
the top two categories are within a whisker of each other, mail gets
`⚠ Needs Review`. A confident wrong label costs more trust than an admitted
unknown.

**Corrections need no new habit.** Change a category in Outlook the normal way;
the tool stamps its own verdict on each message it labels and notices the
divergence on the next run.

**Category descriptions are the model.** There is no training data, so what
separates "Grants" from "Nonprofit Partners" is nothing more than the prose in
[`taxonomy.go`](weekly-summary/internal/labels/taxonomy.go). Each one names real
programs and states explicitly what does *not* belong.

**Scheduled jobs on a laptop need a catch-up rule.** If the machine is asleep at
4pm Friday the task fires whenever it next wakes — so the program works out
which week it *should* have reported, says so in the email, and records what it
sent so a late run cannot duplicate or skip a week.

---

## Security

Application permissions in Microsoft Graph grant access to **every mailbox in
the tenant** by default. For an organization holding donor records that is
unacceptable over-privilege for a summary tool.

[Application Access Policy](https://learn.microsoft.com/en-us/exchange/permissions-exo/application-access-policies)
is still the only supported way to constrain it, and the setup instructions
treat it as mandatory rather than optional. It is the one step that breaks
nothing when skipped, which is exactly why it is easy to skip.

The client secret is stored DPAPI-encrypted against the Windows user account,
so a copied config file is useless on another machine or login.

---

## Getting started

Each tool has its own setup guide:

- [Weekly summary + Claude](weekly-summary/README.md) — Entra registration,
  the access-policy lockdown, building the Windows binary
- [Email sorter add-in](email-sorter/README.md) — app registration, hosting,
  sideloading
- [Testing without a mailbox](weekly-summary/TESTING.md) — what can be verified
  offline and what genuinely needs a real inbox

```bash
cd weekly-summary && go test ./...   # 57 tests, no mailbox needed
cd email-sorter   && npm test        # 34 tests, no mailbox needed
```

---

## Status

The code is tested; **none of it has run against a live mailbox yet.** The
Microsoft Graph client is covered by tests against a stand-in server, and all
analysis logic is pure functions verified against synthetic data — but real
credentials, real Graph responses, and the Windows-specific pieces (DPAPI, the
scheduled task) are unverified.

Treat it as a working reference implementation rather than something proven in
production.

---

## License

MIT — see [LICENSE](LICENSE).

Not affiliated with or endorsed by Microsoft, Anthropic, or Google. Product
names are trademarks of their respective owners.
