# Plan: talking to it from Claude Desktop

Status: **proposed, not built.** Awaiting go-ahead.

## What this is

The director opens Claude Desktop and types "what am I forgetting?" or "sort my
emails". Claude reads her mailbox and answers.

## What this is NOT

It does **not** replace the Friday email. Chat can't be scheduled, so a summary
she has to remember to ask for cannot catch the things she forgot. This is a
complement and a fallback, not a substitute.

Honest limits:

- Only works while Claude Desktop is open.
- Uses her Claude usage allowance.
- If the add-in never gets deployed, this becomes the sorter. It is a decent
  sorter — arguably a smarter one — but it is manual.

---

## How it works

No new codebase. The existing `digest.exe` gains one mode:

```
digest.exe --mcp
```

In that mode it speaks MCP (the protocol Claude Desktop uses to talk to local
programs) over stdin/stdout instead of printing a report. Claude Desktop is
told where the .exe lives, and from then on Claude can call it.

Everything is reused: the Microsoft key, the encrypted secret, the Graph client,
and all the tested analysis logic. Nothing about how mail is read or judged
changes — only how the answer gets delivered.

```
The director types in Claude Desktop
        │
        ▼
   digest.exe --mcp   ◄── same config, same key, same logic
        │
        ▼
  Microsoft Graph (her mailbox)
```

### One genuinely nice consequence

For sorting, **Claude does the classifying itself.** The add-in ships email to
Google's Gemini; here the emails are already in the conversation, so the model
reading them is the one she is talking to. That means:

- no Gemini API key
- no donor mail going to a third free tier
- a better model doing the judging
- she can argue with it: "no, that one's a scholarship thing" and it adjusts
  mid-conversation

---

## What Claude would be able to do

Six tools. Deliberately small and specific — a single "do everything" tool
produces vague results.

**Read-only (works with the key you already need):**

| Tool | She says | What it does |
|---|---|---|
| `whats_waiting` | "what am I forgetting?" | Unanswered mail addressed to her, oldest first |
| `weekly_summary` | "how was my week?" | The full Friday digest, in chat |
| `whats_next_week` | "what's coming up?" | Calendar plus un-RSVP'd invitations |
| `find_mail` | "anything from Tessa?" | Search by sender, subject, or date range |

**Writes labels (needs one extra Microsoft permission):**

| Tool | She says | What it does |
|---|---|---|
| `suggest_labels` | "sort my emails" | Returns unlabelled mail + the categories. Claude proposes. **Changes nothing.** |
| `apply_labels` | "yes, do it" | Writes the labels she approved, by message ID |

### Safety: it always asks first

`suggest_labels` cannot modify anything. Claude shows the proposed labels, she
says yes, and only then does `apply_labels` run — against the specific messages
she agreed to.

This matters more here than in the add-in. In the panel she clicks a specific
chip and one thing happens. In chat, "sort my emails" is vague, and a tool that
silently relabels 200 messages from a vague instruction is exactly the wrong
behaviour. Categories are reversible, but surprise is still bad.

Hard caps: 50 messages per call, and it says how many are left.

---

## The one decision you need to make

| | Read-only | Read + write labels |
|---|---|---|
| Microsoft permissions | `Mail.Read`, `Calendars.Read`, `Mail.Send` | adds `Mail.ReadWrite` |
| She can ask | "what am I forgetting?", "how was my week?" | all of that, plus "sort my emails" |
| If the add-in never ships | no sorting at all | **this becomes the sorter** |
| Risk | can't change anything, ever | can write labels; can't move or delete |

`Mail.ReadWrite` still cannot delete or send mail as her beyond the digest. But
it is a real increase, and the same key sits on her laptop either way.

**My recommendation: read + write.** The add-in is the least certain piece of
this whole project — it needs a deployment you haven't done, and tenant policy
could still block sideloading. If that falls through, read-only leaves you with
no sorter at all. Read+write means the sorter survives.

---

## Setup on her machine

The weak point of this idea is that Claude Desktop is configured by hand-editing
a JSON file. She should never see that.

So the existing installer gets a flag:

```powershell
.\Install-Digest.ps1 -WithClaude
```

which writes `%APPDATA%\Claude\claude_desktop_config.json` for her, **merging**
into whatever is already there rather than overwriting — if she has other Claude
connectors set up, they survive. Then she restarts Claude Desktop and it works.

A matching `Remove Claude Connection.cmd` for the thumb drive.

---

## What it costs her

Rough numbers, per request:

| She asks | Emails read | Approx. tokens |
|---|---|---|
| "what am I forgetting?" | ~30 summarised | 5–8k |
| "how was my week?" | pre-computed summary | 2–3k |
| "sort my emails" | 50 with previews | 25–40k |

A few of these a day is comfortable on a Pro plan. Sorting a 2,000-message
backlog in one go is not — hence the 50-message cap, which turns it into
"sort the next 50" repeated, with a running count.

---

## Work involved

| Piece | Size |
|---|---|
| MCP protocol over stdio | ~150 lines |
| Six tool handlers | ~250 lines, all wrapping existing functions |
| Installer + uninstaller changes | ~60 lines PowerShell |
| Tests (protocol handshake, each tool, the confirm-before-write rule) | ~150 lines |
| One-page guide for her | 1 page |

No new dependencies. No new Microsoft app registration — the same one, with one
permission added if you choose read+write.

**Roughly a day.** The reason it is cheap is that the hard parts — auth, reading
Graph safely, deciding what "waiting on you" means — are already written and
tested.

---

## What could go wrong

| Risk | Reality |
|---|---|
| Claude Desktop changes its config format | Would break the connection; fix is a one-line installer change |
| She hits usage limits mid-sort | The 50-cap makes this unlikely; it reports progress so a stop is recoverable |
| MCP server crashes | Claude reports the tool failed; nothing in the mailbox is touched |
| She asks something vague and it does the wrong thing | Writes always require explicit confirmation first |

---

## My recommendation on timing

Build this **after** one of the other two tools has actually run against a real
mailbox. Not because this is hard, but because all three share the same
Microsoft key — and if that setup is wrong, building a third thing on top of it
just means three broken things instead of one.

Once the key is proven, this is about a day and gives her a way in that needs no
add-in, no sideloading, and no waiting until Friday.
