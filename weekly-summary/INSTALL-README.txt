WEEKLY OUTLOOK SUMMARY - INSTALLATION
=====================================

This puts a small program on this computer that emails you a summary of your
week every Friday afternoon: what's still waiting on a reply, what you never
opened, what your week looked like, and what's coming next week.

Nothing is installed into Outlook itself and nothing is changed in your mailbox.
The program only reads, and sends one email a week.


BEFORE YOU START
----------------

You need three values from the app registration set up in Entra (whoever
prepared this should have sent them to you):

  1. Directory (tenant) ID
  2. Application (client) ID
  3. Client secret value

Keep the secret private - it can read this mailbox.


INSTALLING
----------

1. Put this whole folder somewhere permanent, like your Documents folder.
   Don't run it from a USB stick or the Downloads folder.

2. Right-click "Install-Digest.ps1" and choose "Run with PowerShell".

   If that option isn't there, open PowerShell and run:

       powershell -ExecutionPolicy Bypass -File .\Install-Digest.ps1

3. Windows may show a blue box saying "Windows protected your PC".
   This is expected - the program isn't code-signed.
   Click "More info", then "Run anyway".

4. Answer the prompts. Paste the three values from above when asked.
   For "Mailbox to summarize" enter your own email address.

5. It will say "Access confirmed" and schedule itself for Fridays at 4pm.


SEEING IT WORK RIGHT NOW
------------------------

You don't have to wait until Friday. To create a preview file on your Desktop
without sending anything:

    "%LOCALAPPDATA%\SWCFDigest\digest.exe" --preview "%USERPROFILE%\Desktop\preview.html"

Then double-click preview.html to open it in your browser.

To actually send yourself one immediately:

    "%LOCALAPPDATA%\SWCFDigest\digest.exe" --force


IF THE COMPUTER IS OFF ON FRIDAY
--------------------------------

That's handled. The summary runs the next time you turn the computer on, and it
still covers the correct week - the email will say it's covering last week.


CHANGING WHEN IT RUNS
---------------------

Re-run the installer with a different day or time:

    powershell -ExecutionPolicy Bypass -File .\Install-Digest.ps1 -Day Monday -Time 08:00


TURNING IT OFF
--------------

    powershell -ExecutionPolicy Bypass -File .\Uninstall-Digest.ps1


IF SOMETHING GOES WRONG
-----------------------

There's a log file here:

    %APPDATA%\SWCFDigest\digest.log

Open it and send the last few lines to whoever set this up.


WHAT IT CAN'T SEE
-----------------

It only knows what's in Outlook. It can't see phone calls, site visits, or work
done in the grants database, and it knows how you replied to a meeting invite
but not whether you actually attended. Treat the summary as a prompt for what
still needs attention, not a record of everything you did.
