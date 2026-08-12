# Weekly Outlook Digest

Emails a Friday-afternoon summary of one Outlook mailbox: what's still waiting
on a reply, what never got opened, what the week actually looked like, and
what's coming next week.

Built for the Executive Director of the
[Community Foundation Serving Southwest Colorado](https://www.swcommunityfoundation.org/).

---

## What it can and cannot see

It reads her Outlook mailbox and calendar. That means it can report meetings
that were scheduled and mail that was sent or received.

It **cannot** see:

- whether she actually attended a meeting — only how she RSVP'd
- phone calls, site visits, or hallway conversations
- work done in the grants database, spreadsheets, or anywhere outside Outlook

So the "week in review" section is framed as **activity, not accomplishment**,
and the email says so in its footer. The genuinely valuable section is
**"Still waiting on you"** — mail addressed directly to her, from a human, that
she never answered. That one is precise and hard to get from Outlook by hand.

---

## How it works

A single `digest.exe` (no runtime to install) runs on a Windows machine under
Task Scheduler, Fridays at 4pm.

### The laptop problem, and how it's handled

A desktop app only runs when the computer is on. If the laptop is shut at 4pm
Friday, a naive scheduled job either skips the week silently or, worse, runs
Monday and reports a week that is six hours old.

Three things prevent that:

| Mechanism | Where | Effect |
|---|---|---|
| `-StartWhenAvailable` | Scheduled task | Runs at the next opportunity if the slot was missed |
| `-WakeToRun`, `-AllowStartIfOnBatteries` | Scheduled task | Wakes from sleep; Windows otherwise refuses to run tasks on battery |
| Catch-up window logic | `internal/analyze/window.go` | A run on Mon–Thu reports the **previous** week if that week was never sent |

The last one is the important one. The program works out which week it *should*
have reported, says so in the email header, and records what it sent so a late
run can never duplicate or skip a week.

### Authentication

App-only (client credentials), because there is no user present at 4pm Friday to
complete an interactive sign-in, and refresh tokens on an unattended machine
expire in ways that fail silently weeks later.

The client secret is stored **DPAPI-encrypted** against the Windows user
account, so a copied `config.json` is useless on any other machine or login.

---

## Setup

### 1. Register the app in Entra

The director administers the tenant, so she can do all of this.

1. [Entra portal](https://entra.microsoft.com) → **Applications** → **App registrations** → **New registration**
   - Name: `SWCF Weekly Digest`
   - Supported account types: **this organizational directory only**
   - No redirect URI (it never signs a user in)
2. Copy the **Application (client) ID** and **Directory (tenant) ID**.
3. **API permissions** → **Add a permission** → **Microsoft Graph** → **Application permissions**:

   | Permission | Why |
   |---|---|
   | `Mail.ReadWrite` | read inbox and sent items; apply labels when sorting |
   | `Mail.Send` | send the digest |
   | `Calendars.Read` | read the calendar |

   Then **Grant admin consent**. All three must show a green check.

   > Make sure you're on the **Application permissions** tab, not Delegated.
   > It defaults to Delegated, and that mistake surfaces much later as a 403.

   > `Mail.ReadWrite` is only needed for sorting (via Claude, or the add-in).
   > For the weekly summary alone, `Mail.Read` is enough — and neither permits
   > deleting mail, moving it, or sending as her beyond the digest itself.

   > Note there is no `User.Read.All` here. It isn't needed — the access check
   > reads the inbox folder rather than the directory — and it would grant a
   > tenant-wide directory read this tool has no business holding.

4. **Certificates & secrets** → **New client secret**. Copy the **Value**
   immediately; it is never shown again. Note the expiry date (24 months max) —
   see [Maintenance](#maintenance).

### 2. Lock it to one mailbox

**Do not skip this.** Application permissions grant access to **every mailbox in
the tenant** by default. For a foundation holding donor correspondence, that is
unacceptable over-privilege for a weekly summary tool.

[Application Access Policy](https://learn.microsoft.com/en-us/exchange/permissions-exo/application-access-policies)
is still the only supported way to constrain this. In Exchange Online PowerShell:

```powershell
Connect-ExchangeOnline

# A mail-enabled security group holding only the mailbox to expose.
New-DistributionGroup -Name "SWCF Digest Scope" `
  -Alias swcf-digest-scope `
  -Type Security `
  -PrimarySmtpAddress swcf-digest-scope@example.org `
  -Members director@example.org

# Restrict the app to exactly that group.
New-ApplicationAccessPolicy `
  -AppId "<APPLICATION CLIENT ID>" `
  -PolicyScopeGroupId swcf-digest-scope@example.org `
  -AccessRight RestrictAccess `
  -Description "Weekly digest may read only the ED mailbox"
```

Verify it — this should return `Granted` for her mailbox and `Denied` for anyone
else:

```powershell
Test-ApplicationAccessPolicy -Identity director@example.org -AppId "<CLIENT ID>"
Test-ApplicationAccessPolicy -Identity someone.else@example.org -AppId "<CLIENT ID>"
```

Policy changes can take up to 30 minutes to take effect.

### 3. Build the executable

From macOS or Linux — Go cross-compiles, so no Windows machine is needed:

```bash
./build.sh
```

Produces `dist/windows/digest.exe` plus the installer scripts. Copy the whole
`dist/windows` folder to the Windows machine.

### 4. Install on Windows

```powershell
powershell -ExecutionPolicy Bypass -File .\Install-Digest.ps1
```

It prompts for the tenant ID, client ID, secret, and mailbox, verifies access,
then registers the scheduled task.

### Getting past SmartScreen

The `.exe` is unsigned, so Windows will show *"Windows protected your PC"* on
first run. Click **More info** → **Run anyway**.

Signing it properly costs roughly $200–400/year for a code-signing certificate.
Worth it if this is ever distributed more widely; not worth it for one machine.

---

## Using it

```powershell
digest.exe --check                      # verify access, change nothing
digest.exe --preview out.html           # write the email to a file, don't send
digest.exe --dry-run                    # do everything except send
digest.exe --force                      # send now, even if this week already went
digest.exe                              # what the scheduled task runs
```

Start with `--preview`. It produces the real email from the real mailbox without
sending anything, which is the safest way to tune the settings.

---

## Configuration

`%APPDATA%\SWCFDigest\config.json`

| Field | Default | Notes |
|---|---|---|
| `timezone` | `America/Denver` | Sets the week boundaries. Wrong value shifts the whole window. |
| `alsoAddressedAs` | none | Role aliases and shared mailboxes she also receives at. **Get this wrong and the waiting list silently under-reports** — mail to an unknown alias looks like mail to a stranger. |
| `waitingGraceHours` | `48` | How long mail may sit before it counts as waiting. Lower is naggier. |
| `recipients` | the mailbox | Who receives the digest. |
| `ignoredSenderPatterns` | see below | Senders excluded from "waiting on you". |

Sender patterns are matched three ways, deliberately not as naive substrings:

- `@example.org` — matches the domain
- `news@` — matches the local part, anchored at its start
- `bounce` — plain substring

The anchoring matters: a substring match on `news@` would also swallow
`goodnews@apersonsdomain.org`, and silently dropping a real person from the
waiting list is the worst failure this tool can have.

---

## Development

```bash
go test ./...        # all logic tests, no mailbox needed
go vet ./...
./build.sh
```

The analysis layer is pure functions of its inputs — no network, no ambient
clock — which is what lets the whole report be tested against synthetic data on
a machine that has never touched the target mailbox.

`internal/report/report_test.go` renders a realistic sample week to
`dist/sample-digest.html`. Open it in a browser to see the email.

### Layout

```
cmd/digest/         CLI entry point, setup flow, logging
internal/config/    settings; DPAPI secret storage (+ dev fallback)
internal/graph/     Microsoft Graph client: auth, mail, calendar, sendMail
internal/analyze/   window resolution and the four report sections
internal/report/    HTML email rendering
install/            Windows installer / uninstaller
```

---

## Troubleshooting

Log: `%APPDATA%\SWCFDigest\digest.log`

| Symptom | Cause |
|---|---|
| `HTTP 401` | Client secret expired or wrong. Regenerate in Entra, re-run `--setup`. |
| `HTTP 403` | Admin consent not granted, or the mailbox isn't in the access-policy group. |
| `HTTP 404` | Mailbox address is wrong. |
| `could not decrypt the stored secret` | Config was copied from another machine or user. Re-run `--setup`. |
| No email, no error | Check `state.json` — it may have already sent this week. Use `--force`. |
| Task never runs | It runs as the logged-on user; confirm she signs in on that machine. |

---

## Maintenance

**The client secret expires** (24 months maximum, whatever was chosen at
creation). When it does, the digest stops with a 401 and the log says so. Put a
calendar reminder a month before the expiry date. Renewing means creating a new
secret in Entra and re-running `digest.exe --setup`.

To remove everything:

```powershell
powershell -ExecutionPolicy Bypass -File .\Uninstall-Digest.ps1 -Purge
```

Then delete the app registration in Entra to fully revoke mailbox access —
uninstalling the program does not do that.

---

## Option 2: asking Claude directly

The same `digest.exe` doubles as an MCP server, so Claude Desktop can read the
mailbox and answer questions in plain English.

```powershell
digest.exe --mcp        # what Claude Desktop runs; not for typing at
```

She never edits JSON — `Connect-Claude.ps1` writes Claude's config file,
**merging** into whatever is there so any other connectors survive, and backing
up the original first.

### What Claude can do

| Tool | Reads | Writes |
|---|---|---|
| `whats_waiting` | unanswered mail addressed to her | — |
| `weekly_summary` | the full Friday digest, as text | — |
| `whats_next_week` | calendar and un-RSVP'd invitations | — |
| `find_mail` | search by sender, subject, age | — |
| `read_message` | one full message body | — |
| `list_categories` | the label taxonomy | — |
| `suggest_labels` | unlabelled mail + taxonomy | — |
| `apply_labels` | — | **applies categories** |

Only the last one writes, and its description tells the model, in the text it
actually reads, to get explicit agreement first. `suggest_labels` says
"THIS CHANGES NOTHING" for the same reason. Both facts are covered by tests, in
`cmd/digest/mcp_test.go`.

Sorting here needs `Mail.ReadWrite` on the Entra app. Everything else works
with `Mail.Read`.

### Two design details worth knowing

**Short references.** Graph message IDs are ~150 characters. Round-tripping
fifty of those through a model wastes thousands of tokens, so tools emit `m1`,
`m2` and the server remembers what they point at. Keeping the whole message also
means applying a label needs no second fetch, and lets categories she applied by
hand be preserved rather than overwritten.

**stdout is sacred.** The protocol runs over stdout. A stray `fmt.Println`
anywhere in that path corrupts the stream, and Claude reports the connection as
broken with no useful error. All diagnostics in MCP mode go to stderr.

### Verifying it by hand

```bash
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | digest --mcp
```

Two JSON lines back means the protocol layer is healthy. A notification (no
`id`) must produce no reply at all — replying to one corrupts the stream.

---

## Adding more later

The three delivery paths — Friday email, Claude, and the Outlook add-in — all
sit on one Graph client and one set of analysis functions. Adding a capability
usually means writing it once and exposing it in whichever paths want it.

### A new question Claude can answer

1. Write the logic in `internal/analyze/` as a pure function of its inputs — no
   network, no clock beyond a `now` you pass in. That is what keeps it testable
   without a mailbox.
2. Add a `tool<Name>` method in `cmd/digest/mcp.go` that fetches, calls it, and
   returns text.
3. Add the entry to `toolDefinitions()`, and the case to `callTool`.
4. Add the tool name to the list in `TestToolSchemasAreWellFormed`.

The description field is the whole interface. It is what decides whether the
model reaches for the tool at the right moment, so write it as instructions to a
colleague, not as a label.

### A new section in the Friday email

Add the function in `internal/analyze/`, a field on `Digest`, a block in
`internal/report/template.html`, and a line in `renderDigest` in `mcp.go` so
both paths stay in step. Tables and inline styles only — Outlook on Windows
renders through Word.

### New Graph data (tasks, contacts, files)

`internal/graph/` is a thin REST client. A new resource is one method following
the shape of `CalendarView`, plus the matching Application permission in Entra
and consent. Likely candidates:

| Want | Permission | Endpoint |
|---|---|---|
| To-Do tasks | `Tasks.Read` | `/users/{id}/todo/lists` |
| Contacts | `Contacts.Read` | `/users/{id}/contacts` |
| Files in OneDrive | `Files.Read.All` | `/users/{id}/drive` |
| Teams messages | `Chat.Read.All` | `/users/{id}/chats` |

Adding a permission means re-consenting in Entra, and each one widens what the
key on her laptop can reach — so add them one at a time and only when something
actually needs them.

### Changing the categories

`internal/labels/taxonomy.go`. The descriptions **are** the classifier — there
is no training data — so each one names real programs and states explicitly what
does *not* belong. Vague descriptions are the main cause of bad sorting. The
add-in keeps its own copy in `src/taxonomy.ts`; keep the two in step if you
change one.

---

## Testing against your own mailbox

App-only authentication **cannot** be used with a personal Microsoft account —
Microsoft excludes personal accounts from the client-credentials flow entirely.
For testing, or for any mailbox where app-only is unavailable, sign in as
yourself instead:

```bash
digest --login
```

It prints a code, you enter it at `microsoft.com/devicelogin`, and that is the
whole flow. No client secret, no admin consent.

You need one Entra app registration, but a minimal one:

1. [Entra portal](https://entra.microsoft.com) → **App registrations** → **New**
2. Supported account types: **any organizational directory and personal Microsoft accounts**
3. **Authentication** → **Allow public client flows** → **Yes**
4. Copy the Application (client) ID — that is all `--login` asks for

Delegated permissions (`Mail.ReadWrite`, `Mail.Send`, `Calendars.Read`) are
consented by you at sign-in; no administrator is involved.

### Why this is not the default

Delegated refresh tokens expire — through inactivity, a password change, or a
conditional-access policy — and they do it silently, weeks later, on a machine
nobody is watching. That is precisely the failure mode a weekly unattended job
must not have, which is why the scheduled digest uses app-only auth and this
mode exists for testing and as a fallback.

When the saved sign-in does expire, the error says `Run: digest --login`
rather than surfacing a raw OAuth code.
