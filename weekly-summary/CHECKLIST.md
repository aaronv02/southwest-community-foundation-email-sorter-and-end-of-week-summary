# Step-by-step checklist

Three phases. Do them in order — Phase 1 proves the tool works before you touch
a real mailbox, which means Phase 2 is a security exercise rather than a
debugging session.

---

# PHASE 1 — Prove it works on your own mailbox

**~15 minutes. Free. No credit card. Nothing here touches her mailbox.**

## 1.1 Get an Outlook account

- [ ] Create a free account at [outlook.com](https://outlook.com)
- [ ] Send it 3–4 emails from another address so there's something to find
      *(a totally empty inbox makes it impossible to tell "working" from "broken")*

## 1.2 Register the app

- [ ] Go to [entra.microsoft.com](https://entra.microsoft.com), sign in with that account
- [ ] **Applications** → **App registrations** → **New registration**
- [ ] Name: anything, e.g. `Digest Test`
- [ ] Supported account types: **Accounts in any organizational directory and personal Microsoft accounts**
      ⚠️ This exact option. The others will reject a personal account.
- [ ] Redirect URI: leave empty
- [ ] **Register**
- [ ] **Authentication** (left menu) → scroll to **Advanced settings** →
      **Allow public client flows** → **Yes** → **Save**
      ⚠️ Easy to miss, and sign-in fails without it
- [ ] **Overview** → copy the **Application (client) ID**

> No client secret. No API permissions. Nothing else to configure.

## 1.3 Sign in

```bash
cd "/Users/aaronv/Documents/swcommunityfoundation weekly digest" && go run ./cmd/digest --login
```

- [ ] Paste the client ID when asked
- [ ] Tenant: press Enter to accept `common`
- [ ] Email address: your new outlook.com address
- [ ] Open the URL it prints, enter the code, approve
- [ ] Confirm it says **"Signed in. Checking mailbox access… confirmed."**

## 1.4 Look at the summary

```bash
cd "/Users/aaronv/Documents/swcommunityfoundation weekly digest" && go run ./cmd/digest --preview week.html && open week.html
```

- [ ] The email opens in your browser
- [ ] Sections appear: Still waiting on you / You didn't open these / What your week looked like / Calendar

**This sends nothing.** It only writes a file.

## 1.5 Sanity-check the output

The one number that tells you everything is the length of **"Still waiting on you."**

| What you see | What it means |
|---|---|
| A few real emails, oldest first | ✅ working |
| Empty, but you have unanswered mail | aliases wrong — see Troubleshooting |
| Full of `noreply@` senders | ignore list needs their vendors |
| Everything errored | see Troubleshooting |

- [ ] Output looks broadly right

**Stop here for now.** You've proven the tool works. Phase 2 is a different job.

---

# PHASE 2 — Set up her mailbox

**~25 minutes. Do this with her, or with her admin credentials.**
**Only after Phase 1 succeeded.**

## 2.1 Register the production app *(in HER tenant, signed in as her)*

- [ ] [entra.microsoft.com](https://entra.microsoft.com) → **App registrations** → **New registration**
- [ ] Name: `SWCF Weekly Digest`
- [ ] Supported account types: **Accounts in this organizational directory only**
- [ ] No redirect URI → **Register**
- [ ] Copy the **Application (client) ID** ➜ write it on the credentials card
- [ ] Copy the **Directory (tenant) ID** ➜ write it on the card

## 2.2 Permissions

- [ ] **API permissions** → **Add a permission** → **Microsoft Graph**
- [ ] Choose **Application permissions**
      ⚠️ **NOT Delegated.** It defaults to Delegated. This is the single most
      common mistake and it surfaces later as a confusing 403.
- [ ] Add: `Mail.ReadWrite`, `Mail.Send`, `Calendars.Read`
- [ ] Click **Grant admin consent for [org]**
- [ ] Confirm all three show a **green check**

## 2.3 Client secret

- [ ] **Certificates & secrets** → **New client secret**
- [ ] Description: `weekly digest`, expiry: 24 months
- [ ] Copy the **Value** column ➜ write it on the card
      ⚠️ The **Value**, not the Secret ID. It is shown once and never again.
- [ ] Write the expiry date on the card
- [ ] Put a calendar reminder **one month before** that date

## 2.4 Lock it to her mailbox only — DO NOT SKIP

Without this, that secret can read **every mailbox in the foundation**.
Skipping it breaks nothing, which is exactly why it gets skipped.

In PowerShell:

```powershell
Install-Module ExchangeOnlineManagement -Scope CurrentUser   # first time only
Connect-ExchangeOnline
```

```powershell
New-DistributionGroup -Name "SWCF Digest Scope" `
  -Alias swcf-digest-scope -Type Security `
  -PrimarySmtpAddress swcf-digest-scope@example.org `
  -Members director@example.org
```

```powershell
New-ApplicationAccessPolicy `
  -AppId "<CLIENT ID FROM 2.1>" `
  -PolicyScopeGroupId swcf-digest-scope@example.org `
  -AccessRight RestrictAccess `
  -Description "Weekly digest may read only the ED mailbox"
```

- [ ] Both commands succeeded
- [ ] Verify — first should say **Granted**, second **Denied**:

```powershell
Test-ApplicationAccessPolicy -Identity director@example.org -AppId "<CLIENT ID>"
Test-ApplicationAccessPolicy -Identity <any other staff member> -AppId "<CLIENT ID>"
```

- [ ] Denied really said Denied *(if not, wait 30 min and retest — policy takes time to apply)*

## 2.5 Find out her other addresses

- [ ] Ask her: **"Besides director@, what other addresses reach you?"**
      e.g. `info@`, `grants@`, a shared mailbox

Write them down. If this is wrong, "Still waiting on you" silently
under-reports and there is no error to tell you why.

---

# PHASE 3 — Install on her computer

**~10 minutes, at her machine.**

## 3.1 Get the files there

- [ ] Remove the `_For whoever set this up` folder from the zip first
- [ ] Send her `SWCF-Outlook-Tools.zip`, or bring it on a USB stick
- [ ] **Right-click the ZIP → Extract All** *(don't run from inside the zip)*
- [ ] Move the extracted folder to **Documents**, not Downloads

## 3.2 Install

- [ ] Double-click **`Install Weekly Summary.cmd`**
- [ ] At *"Windows protected your PC"*: **More info** → **Run anyway**
- [ ] Enter tenant ID, client ID, secret from the card
- [ ] Mailbox: her address
- [ ] **Other addresses**: the aliases from 2.5
- [ ] Timezone: `America/Denver`
- [ ] Confirm it says **"Access confirmed"**

## 3.3 Check before letting it send

- [ ] Double-click **`Preview This Week.cmd`**
- [ ] It opens a real summary of her actual week
- [ ] "Still waiting on you" looks right *(5–15 real items, oldest first)*
- [ ] If it's empty → the aliases are wrong, fix and re-run
- [ ] If it's full of robots → note the senders, add them to `ignoredSenderPatterns`

**Only continue once the preview looks right.**

## 3.4 Turn it on

- [ ] Nothing more to do — the installer already scheduled it for Friday 4pm
- [ ] Optional, to see one now: run `digest.exe --force`

## 3.5 Optionally connect Claude

- [ ] Confirm Claude Desktop is installed and she's signed in at least once
- [ ] Double-click **`Connect To Claude.cmd`**
- [ ] **Quit Claude completely** — system tray, right-click, Quit — then reopen
- [ ] Ask it: *"what emails am I forgetting?"*
- [ ] Point her at **`Talking To Claude.txt`**

## 3.6 Tell her the honest parts

- [ ] It only sees Outlook — not calls, site visits, or the grants database
- [ ] It knows how she RSVP'd, not whether she attended
- [ ] Asking Claude about her email puts those messages in the conversation,
      donor names included
- [ ] The secret expires on the date on the card

---

# Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `AADSTS7000215` / 401 | Copied the Secret **ID**, not the **Value** | New secret, copy the Value column |
| 403 | On the **Delegated** tab instead of **Application** | Redo 2.2, check for green ticks |
| 403 after consent | Mailbox not in the access-policy group | Check 2.4, wait 30 min |
| 404 | Mailbox address typo | Re-run `--setup` |
| "Still waiting" empty | Aliases missing | Add them to `alsoAddressedAs` in config.json |
| Full of no-reply senders | Ignore list too narrow | Add their vendors to `ignoredSenderPatterns` |
| Sign-in fails in Phase 1 | Public client flows not enabled | Redo 1.2's Authentication step |
| Claude can't see the tools | Claude not fully quit | Quit from the system tray, not the window |
| Nothing on Friday | PC was off | It runs at next startup and says "Last week" |

Log file: `%APPDATA%\SWCFDigest\digest.log`

---

# Deliberately not on this list

- **The Outlook add-in panel.** Never deployed, needs hosting plus sideloading
  her tenant might block. "Sort my emails" in Claude does the same job. Its
  folder in the zip is marked NOT READY.
- **A Copilot licence** (~$306/yr nonprofit). Worth revisiting later — it would
  let her create and change Outlook rules by asking, with no dependency on you.
  Not needed for any of the above.
