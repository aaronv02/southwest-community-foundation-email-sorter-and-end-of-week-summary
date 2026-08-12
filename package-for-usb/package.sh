#!/usr/bin/env bash
#
# Assembles a thumb-drive-ready folder containing both Outlook tools.
#
# Run this, then drag "output/SWCF Outlook Tools" onto a USB stick.
#
# The two tools are not equally ready to ship, and this script knows the
# difference:
#
#   Weekly Summary  - a self-contained .exe. Ships as soon as it is built.
#                     The director enters the Entra values during install.
#
#   Inbox Sorter    - an Outlook add-in whose taskpane must be hosted at a real
#                     HTTPS address before the manifest means anything. Until
#                     that deployment happens the manifest points at a domain
#                     that does not exist, so this script detects the
#                     placeholder and quarantines the folder rather than
#                     handing over something that fails on her machine.
set -euo pipefail

cd "$(dirname "$0")"

DIGEST_SRC="../weekly-summary"
SORTER_SRC="../email-sorter"

OUT="output/SWCF Outlook Tools"
PLACEHOLDER_HOST="inbox-steward.pages.dev"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }

# Windows reads .txt and .cmd far more happily with CRLF line endings; Notepad
# renders LF-only files as one long line.
crlf() { perl -pi -e 's/(?<!\r)\n/\r\n/' "$@"; }

bold "==> Checking sources"
[ -d "$DIGEST_SRC" ] || { red "Missing: $DIGEST_SRC"; exit 1; }
[ -d "$SORTER_SRC" ] || { red "Missing: $SORTER_SRC"; exit 1; }

rm -rf output
mkdir -p "$OUT"

# ---------------------------------------------------------------------------
# 1. Weekly Summary - build and stage
# ---------------------------------------------------------------------------

bold "==> Building the Weekly Summary executable"
(
  cd "$DIGEST_SRC"
  export PATH="/opt/homebrew/bin:$PATH"
  ./build.sh >/dev/null
)

mkdir -p "$OUT/Weekly Summary"
for f in digest.exe Install-Digest.ps1 Uninstall-Digest.ps1 \
         Connect-Claude.ps1 Disconnect-Claude.ps1; do
  cp "$DIGEST_SRC/dist/windows/$f" "$OUT/Weekly Summary/"
done
cp "$DIGEST_SRC/INSTALL-README.txt" "$OUT/Weekly Summary/README.txt"
crlf "$OUT/Weekly Summary/README.txt"

cp "$DIGEST_SRC/CLAUDE-GUIDE.txt" "$OUT/Talking To Claude.txt"
crlf "$OUT/Talking To Claude.txt"
green "    digest.exe staged"

# A double-clickable wrapper. Right-click -> "Run with PowerShell" is a step too
# many, and it silently does nothing when the execution policy blocks scripts.
cat > "$OUT/Install Weekly Summary.cmd" <<'CMD'
@echo off
title Install Weekly Outlook Summary
cd /d "%~dp0Weekly Summary"

echo.
echo  Installing the Weekly Outlook Summary
echo  =====================================
echo.
echo  You will be asked for three values. Whoever set this up
echo  should have sent them to you:
echo.
echo    - Directory (tenant) ID
echo    - Application (client) ID
echo    - Client secret
echo.
echo  Then enter your own email address as the mailbox.
echo.
pause

powershell -NoProfile -ExecutionPolicy Bypass -File ".\Install-Digest.ps1"

echo.
if errorlevel 1 (
  echo  Something went wrong. Send the messages above to whoever set this up.
) else (
  echo  Done. Your first summary arrives Friday afternoon.
  echo.
  echo  To see one right now without waiting, close this window and
  echo  double-click "Preview This Week.cmd".
)
echo.
pause
CMD
crlf "$OUT/Install Weekly Summary.cmd"

cat > "$OUT/Preview This Week.cmd" <<'CMD'
@echo off
title Preview This Week's Summary
set "EXE=%LOCALAPPDATA%\SWCFDigest\digest.exe"

if not exist "%EXE%" (
  echo.
  echo  The Weekly Summary is not installed yet.
  echo  Double-click "Install Weekly Summary.cmd" first.
  echo.
  pause
  exit /b 1
)

echo.
echo  Building a preview of this week. Nothing will be sent.
echo.
"%EXE%" --preview "%USERPROFILE%\Desktop\Outlook Summary Preview.html"

if errorlevel 1 (
  echo.
  echo  Could not build the preview. See the message above.
  echo.
  pause
  exit /b 1
)

start "" "%USERPROFILE%\Desktop\Outlook Summary Preview.html"
echo.
echo  Opened in your browser, and saved to your Desktop.
echo.
pause
CMD
crlf "$OUT/Preview This Week.cmd"

cat > "$OUT/Connect To Claude.cmd" <<'CMD'
@echo off
title Connect Outlook to Claude
cd /d "%~dp0Weekly Summary"

echo.
echo  Connecting your Outlook to Claude
echo  =================================
echo.
echo  After this, you can open Claude and just ask things like:
echo.
echo    "what emails am I forgetting?"
echo    "how was my week?"
echo    "sort my emails"
echo.
echo  Requires the Weekly Summary to be installed first.
echo.
pause

powershell -NoProfile -ExecutionPolicy Bypass -File ".\Connect-Claude.ps1"

echo.
if errorlevel 1 (
  echo  Something went wrong. Send the messages above to whoever set this up.
)
echo.
pause
CMD
crlf "$OUT/Connect To Claude.cmd"

cat > "$OUT/Disconnect From Claude.cmd" <<'CMD'
@echo off
title Disconnect Outlook from Claude
cd /d "%~dp0Weekly Summary"
echo.
echo  This stops Claude from being able to read your Outlook.
echo  Your weekly summary emails are NOT affected and keep coming.
echo.
pause
powershell -NoProfile -ExecutionPolicy Bypass -File ".\Disconnect-Claude.ps1"
echo.
pause
CMD
crlf "$OUT/Disconnect From Claude.cmd"

cat > "$OUT/Remove Weekly Summary.cmd" <<'CMD'
@echo off
title Remove Weekly Outlook Summary
cd /d "%~dp0Weekly Summary"
echo.
echo  This stops the weekly summary emails and removes the program.
echo  Your saved settings are kept in case you reinstall.
echo.
pause
powershell -NoProfile -ExecutionPolicy Bypass -File ".\Uninstall-Digest.ps1"
echo.
pause
CMD
crlf "$OUT/Remove Weekly Summary.cmd"

# ---------------------------------------------------------------------------
# 2. Inbox Sorter - stage only if it has actually been deployed
# ---------------------------------------------------------------------------

SORTER_READY=0
SORTER_HOST="$(grep -o 'https://[a-z0-9.-]*' "$SORTER_SRC/manifest.xml" \
  | grep -v login.microsoftonline.com | head -1 || true)"

if [ -n "$SORTER_HOST" ] && [[ "$SORTER_HOST" != *"$PLACEHOLDER_HOST"* ]]; then
  SORTER_READY=1
fi

if [ "$SORTER_READY" -eq 1 ]; then
  bold "==> Staging the Inbox Sorter (deployed at $SORTER_HOST)"
  SORTER_DIR="$OUT/Inbox Sorter"
else
  bold "==> Inbox Sorter is NOT deployed yet - quarantining it"
  SORTER_DIR="$OUT/_Inbox Sorter (NOT READY - do not install)"
fi

mkdir -p "$SORTER_DIR"
cp "$SORTER_SRC/manifest.xml" "$SORTER_DIR/"

cat > "$SORTER_DIR/HOW TO INSTALL.txt" <<'TXT'
INBOX SORTER - INSTALLATION
===========================

This adds a panel inside Outlook that labels your mail with colored
categories, learns from your corrections, and gradually teaches Outlook's own
rules to keep doing it automatically.

Nothing is installed on your computer. It lives inside Outlook itself and
follows you to every device you use.


INSTALLING
----------

You need to do this in Outlook on the web, in a browser. It then appears
everywhere else automatically.

1. Open this page in your browser:

       https://aka.ms/olksideload

   Sign in with your work account if it asks.

2. A window called "Add-Ins for Outlook" opens.

3. Click "My add-ins" on the left.

4. Scroll down to the section called "Custom Addins".

5. Click "Add a custom add-in", then choose "Add from File".

6. Pick the file "manifest.xml" from this same folder.

7. Click "Install" when it warns you the add-in is from an unknown source.
   That warning is expected - it means the add-in came from a file rather
   than the Microsoft store.

8. Close the window.


USING IT
--------

Open any email. In the toolbar at the top you'll see a button called
"Sort inbox". Click it to open the panel.

The first time, press "Sort my inbox". It sets up your labels and sorts what
it can.

If it labels something wrong, just change the category the normal way in
Outlook. It notices and never makes that mistake with that sender again.


NOTES
-----

- Desktop Outlook on Windows can take up to 24 hours to show a newly added
  add-in. Outlook on the web shows it immediately.

- To remove it, go back to https://aka.ms/olksideload, find it under
  Custom Addins, and choose Remove.
TXT
crlf "$SORTER_DIR/HOW TO INSTALL.txt"

if [ "$SORTER_READY" -eq 0 ]; then
  cat > "$SORTER_DIR/DO NOT INSTALL YET.txt" <<'TXT'
THIS TOOL IS NOT READY TO INSTALL
=================================

Do not follow the instructions in this folder yet. The add-in has not been
published to a web address, so installing it now would add a broken panel to
Outlook.

Whoever set this up still needs to deploy it. Once they do, they will send a
replacement folder.

The Weekly Summary in the folder above IS ready - that one you can install.
TXT
  crlf "$SORTER_DIR/DO NOT INSTALL YET.txt"
fi

# ---------------------------------------------------------------------------
# 3. Top-level instructions for the director
# ---------------------------------------------------------------------------

bold "==> Writing instructions"

CLAUDE_BLURB='  ASK CLAUDE ABOUT YOUR EMAIL  (optional, do this second)
     Lets you open Claude and just ask, in your own words:

         "what emails am I forgetting?"
         "how was my week?"
         "anything from Tessa this month?"
         "sort my emails"

     Claude reads your mail and answers. It always shows you what it plans
     to label and waits for you to say yes before changing anything.

     -> Double-click "Connect To Claude.cmd"
     -> Then read "Talking To Claude.txt"'

if [ "$SORTER_READY" -eq 1 ]; then
  TOOLS_BLURB="Three things in here. Install them in any order.

  1. WEEKLY SUMMARY
     Emails you a summary every Friday afternoon: what is still waiting on a
     reply, what you never opened, what your week looked like, and what is
     coming next week.

     -> Double-click \"Install Weekly Summary.cmd\"

  2. INBOX SORTER
     Adds a panel inside Outlook that labels your mail with colored categories
     and learns from your corrections.

     -> Open the \"Inbox Sorter\" folder and follow \"HOW TO INSTALL.txt\"

  3.
$CLAUDE_BLURB"
else
  TOOLS_BLURB="Two things are ready to install right now.

  1. WEEKLY SUMMARY
     Emails you a summary every Friday afternoon: what is still waiting on a
     reply, what you never opened, what your week looked like, and what is
     coming next week.

     -> Double-click \"Install Weekly Summary.cmd\"

  2.
$CLAUDE_BLURB

  A third tool, the Inbox Sorter panel inside Outlook, is still being
  finished. Its folder starts with an underscore and is marked NOT READY -
  please ignore it for now. Until it is ready, asking Claude to \"sort my
  emails\" does the same job."
fi

cat > "$OUT/START HERE.txt" <<TXT
OUTLOOK TOOLS
=============

$TOOLS_BLURB


BEFORE YOU START
----------------

Copy this whole folder off the USB stick and onto your computer first -
somewhere permanent like your Documents folder. Installing directly from the
USB stick will break things later when the stick is removed.


IF WINDOWS WARNS YOU
--------------------

You may see a blue box saying "Windows protected your PC", or a warning that
the file came from another computer.

That is expected. These tools were made for you rather than bought from a
store, so Windows has no certificate to check them against.

Click "More info", then "Run anyway".


WHAT THESE CAN SEE
------------------

Only what is in Outlook. The weekly summary knows about meetings on your
calendar and mail in your mailbox. It cannot see phone calls, site visits, or
work done in the grants database, and it knows how you replied to a meeting
invite but not whether you actually attended.

Treat the summary as a prompt for what still needs attention, not a record of
everything you did.


IF SOMETHING GOES WRONG
-----------------------

The weekly summary keeps a log here:

    %APPDATA%\SWCFDigest\digest.log

Open it and send the last few lines to whoever set this up.
TXT
crlf "$OUT/START HERE.txt"

# ---------------------------------------------------------------------------
# 4. Notes for whoever is handing this over
# ---------------------------------------------------------------------------

mkdir -p "$OUT/_For whoever set this up"
NOTES="$OUT/_For whoever set this up"

if [ -f "$DIGEST_SRC/dist/sample-digest.html" ]; then
  cp "$DIGEST_SRC/dist/sample-digest.html" "$NOTES/what the email looks like.html"
fi

cat > "$NOTES/BEFORE YOU HAND THIS OVER.txt" <<TXT
CHECKLIST - complete these before giving her the USB stick
==========================================================

WEEKLY SUMMARY
--------------

The .exe is built and ready. She enters the credentials during install, so
you must give her three values on the card in this folder:

  [ ] Register an Entra app: Applications > App registrations > New
      Name: SWCF Weekly Digest
      Accounts: this organizational directory only
      No redirect URI

  [ ] API permissions > Microsoft Graph > APPLICATION permissions
      (NOT Delegated - the tab defaults to Delegated and it is the single
      most common setup mistake; it fails later with a 403):

        Mail.ReadWrite  (read mail, and apply colored labels)
        Mail.Send       (send the digest)
        Calendars.Read  (read the calendar)

      Then click "Grant admin consent". All three need green checks.

      Note on Mail.ReadWrite: the weekly summary alone only needs Mail.Read.
      The write permission is what lets her say "sort my emails" to Claude.
      It still cannot delete mail, move it between folders, or send as her
      beyond the digest itself. If you decide against sorting, use Mail.Read
      instead and everything else still works.

  [ ] Certificates & secrets > New client secret. Copy the VALUE immediately -
      it is never shown again. Note the expiry date.

  [ ] LOCK IT TO HER MAILBOX. Do not skip this. Application permissions reach
      EVERY mailbox in the tenant by default, which is far too much for a
      summary tool at a foundation holding donor records.

      In Exchange Online PowerShell:

        Connect-ExchangeOnline

        New-DistributionGroup -Name "SWCF Digest Scope" \\
          -Alias swcf-digest-scope -Type Security \\
          -PrimarySmtpAddress swcf-digest-scope@example.org \\
          -Members director@example.org

        New-ApplicationAccessPolicy -AppId "<CLIENT ID>" \\
          -PolicyScopeGroupId swcf-digest-scope@example.org \\
          -AccessRight RestrictAccess \\
          -Description "Weekly digest may read only the ED mailbox"

      Verify - the first should say Granted, the second Denied:

        Test-ApplicationAccessPolicy -Identity director@example.org -AppId "<CLIENT ID>"
        Test-ApplicationAccessPolicy -Identity <someone else> -AppId "<CLIENT ID>"

      Allow up to 30 minutes for the policy to take effect.

  [ ] Fill in "credentials card.txt" in this folder and hand it over
      separately from the USB stick - ideally not by email.

  [ ] Put a calendar reminder one month before the secret expires. When it
      expires the digest stops with a 401 and she gets nothing.


ASK CLAUDE (the second option)
------------------------------

Nothing extra to register - it reuses the same app and the same key. Once
the Weekly Summary is installed and working, she double-clicks
"Connect To Claude.cmd" and restarts Claude Desktop.

  [ ] Confirm Claude Desktop is installed and she has signed in at least once.
      The connector writes to %APPDATA%\Claude, which only exists after that.

  [ ] Verify it yourself first if you can: run
        digest.exe --mcp
      It should print "ready for mailbox ..." to the screen and then sit
      waiting for input. Press Ctrl+C. If it prints an error instead, the
      mailbox settings are wrong and Claude would fail the same way.

  [ ] Point her at "Talking To Claude.txt" - it lists what to actually say.

Worth knowing: when she asks Claude about her email, the messages it reads
become part of that conversation, donor names and amounts included. That is
the same as pasting them in by hand, but she should know it. It is stated
plainly at the end of her guide.


INBOX SORTER
------------

TXT

if [ "$SORTER_READY" -eq 1 ]; then
  cat >> "$NOTES/BEFORE YOU HAND THIS OVER.txt" <<TXT
Deployed at: $SORTER_HOST
The manifest in the "Inbox Sorter" folder points there. Confirm the site
loads over HTTPS before handing this over.
TXT
else
  cat >> "$NOTES/BEFORE YOU HAND THIS OVER.txt" <<TXT
NOT READY. The manifest still points at the placeholder domain
$PLACEHOLDER_HOST, which does not exist. Its folder is quarantined in this
package and marked "do not install".

To finish it:

  [ ] Register a SECOND Entra app for the add-in (separate from the digest -
      different auth model entirely; that one is app-only, this one is
      delegated via Nested App Authentication).
      Add an SPA redirect URI of:  brk-multihub://<your-domain>
      Delegated permissions: Mail.ReadWrite, MailboxSettings.ReadWrite, User.Read

  [ ] Put the client ID in .env as VITE_ENTRA_CLIENT_ID, then: npm run build

  [ ] Deploy the dist/ folder to an HTTPS host (Cloudflare Pages free tier).
      Use a dedicated domain, NOT a shared *.github.io subdomain - the NAA
      redirect URI authorizes the whole origin.

  [ ] Replace every occurrence of $PLACEHOLDER_HOST in manifest.xml with the
      real host.

  [ ] Re-run package.sh. It will detect the real host and include the folder
      properly instead of quarantining it.
TXT
fi

crlf "$NOTES/BEFORE YOU HAND THIS OVER.txt"

cat > "$NOTES/credentials card.txt" <<'TXT'
WEEKLY SUMMARY - SETUP VALUES
=============================

Fill these in, then give this to her separately from the USB stick.
Do not email the secret if you can avoid it.


  Directory (tenant) ID:  ________________________________________

  Application (client) ID: _______________________________________

  Client secret:  ________________________________________________

  Mailbox:  director@example.org

  Secret expires on:  ____________________________________________


She will be asked for these when she double-clicks
"Install Weekly Summary.cmd".
TXT
crlf "$NOTES/credentials card.txt"

# ---------------------------------------------------------------------------

SIZE=$(du -sh "$OUT" | cut -f1)

echo
green "Package built: $(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")  ($SIZE)"
echo
echo "Drag that folder onto a USB stick."
echo
if [ "$SORTER_READY" -eq 0 ]; then
  red "NOTE: the Inbox Sorter is quarantined - it has not been deployed yet."
  red "      See '_For whoever set this up/BEFORE YOU HAND THIS OVER.txt'."
  echo
fi
bold "Still to do before handing it over:"
echo "  - Register the Entra app and grant admin consent"
echo "  - Restrict it to her mailbox with an ApplicationAccessPolicy"
echo "  - Fill in the credentials card"
