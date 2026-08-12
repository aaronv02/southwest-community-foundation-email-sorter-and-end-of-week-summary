# Inbox Steward — how to use it

A one-page guide. Nothing here can break your mailbox.

---

## What it does

It puts **colored labels** on your email — Grants, Donors & Gifts, Board & Governance, Events, and so on — so you can see at a glance what's in front of you.

It does **not** move, file, archive, or delete anything. Every message stays exactly where it is. The labels are just tags, and you can change or remove any of them at any time.

---

## Using it

Open **Inbox Steward** from the Outlook ribbon and press **Sort my inbox**. That's the whole thing.

Each message it labels shows up in the panel with up to three suggested labels. Click any one to change it.

Anything it isn't confident about gets labelled **⚠ Needs Review** rather than guessed at. That's deliberate — a wrong label is worse than an honest "I don't know."

---

## Correcting it (this is what makes it good)

**Just change the label in Outlook the way you normally would.** Right-click the message, pick the category you actually wanted. You don't need to open the add-in to do it.

Next time it runs, it notices you disagreed and learns from it. Specifically:

- That sender is now permanently correct — it will never get that one wrong again.
- Similar mail from *new* senders gets better too.

So the first week or two you'll be correcting it a fair bit, and then it quiets down. That's the design working, not a fault.

---

## It gets faster and eventually runs itself

Once you've confirmed a sender a few times, Inbox Steward hands that sender over to **Outlook's own rules**. After that, mail from them gets labelled the instant it arrives — even if you never open the add-in again.

You can see everything that's been handed off in the **Automation** tab. Those are ordinary Outlook rules, visible under Outlook's own Rules settings, and you can delete any of them yourself if you disagree.

The practical upshot: **for new or unusual senders, open the panel and press the button. For everything routine, it's already done before you look.**

---

## Changing the labels

The **Categories** tab lets you rename anything, delete labels you don't want, and add your own.

Each label has a **description** underneath. That description is what the sorter actually reads to decide what belongs there, so if something keeps landing in the wrong place, make the description more specific. Concrete beats vague — naming actual programs, funds, or types of sender works far better than a one-word label.

After renaming or adding a category, press **Create these in Outlook** so the label exists in your mailbox.

---

## One thing you should know

To sort mail from senders it doesn't recognize yet, the add-in sends that message's sender, subject, and roughly the first few sentences to Google's AI service.

It is currently set to the **free** tier of that service. Google's terms for the free tier say submitted content may be used to improve their products and may be reviewed by people at Google. For your mailbox that can include donor names, gift amounts, and grant discussions.

You may be entirely fine with that, or not — it's your call to make, not ours, which is why it's a setting rather than a decision baked in. Under **Settings → How much to send for sorting** you have three choices:

| Setting | What leaves your mailbox |
|---|---|
| Subject, sender, and a short preview | Best accuracy. Message text goes to Google. *(current setting)* |
| **Subject and sender only** | No message text, ever. Still works well. |
| Nothing | Only senders it has already learned get labelled. |

Switching to "subject and sender only" costs a little accuracy and keeps message bodies entirely inside your mailbox. There's also a paid tier of the AI service — a few dollars a month — whose terms exclude using your content for training; whoever set this up for you can switch it over.

---

## If something looks wrong

- **Labels aren't appearing.** Categories tab → **Create these in Outlook**, then sort again.
- **It stopped partway.** You've likely hit the free daily limit of the AI service. It resets overnight, and known senders keep being labelled in the meantime.
- **Everything is Needs Review.** The API key in Settings is probably missing or expired.
- **A label is wrong.** Just change it in Outlook. That's the fix and the training in one action.
- **You want to undo everything.** Deleting the categories from Outlook's category list removes every label it applied. Deleting the "Inbox Steward:" rules in Outlook's Rules settings stops the automatic sorting.
