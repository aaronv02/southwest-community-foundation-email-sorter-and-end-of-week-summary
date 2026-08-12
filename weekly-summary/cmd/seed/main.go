// Command seed fills a TEST mailbox with a realistic week of foundation mail
// and calendar entries, so the digest can be run against real Graph data.
//
// This is the answer to "how do we test without her mailbox": rather than
// hand-writing JSON fixtures, inject the fake mail into a real Microsoft 365
// developer-tenant mailbox and let the digest read it the same way it will read
// hers.
//
//	seed --inject               fill the mailbox with a week of mail and events
//	seed --threads --from X     send REAL mail from mailbox X, creating genuine
//	                            conversation threads (needed for reply detection)
//	seed --wipe                 delete everything this tool created
//
// SAFETY: refuses to run unless --i-know-this-is-a-test-mailbox is passed, and
// refuses outright on the production mailbox address.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
)

// Mailboxes this tool must never touch, whatever the flags say.
//
// SET THIS BEFORE USE. Seeding writes junk mail into a mailbox and --wipe
// deletes from one; either against a live mailbox would be unrecoverable. The
// placeholder below protects nothing, so either edit this list or set the
// environment variable:
//
//	PROTECTED_MAILBOX=real.person@yourdomain.org
var protectedMailboxes = []string{
	"director@example.org", // placeholder - replace with the real mailbox
}

// protectedList returns the compiled-in list plus anything named in the
// PROTECTED_MAILBOX environment variable, so the guard can be set without
// editing and rebuilding.
func protectedList() []string {
	out := append([]string{}, protectedMailboxes...)
	for _, extra := range strings.Split(os.Getenv("PROTECTED_MAILBOX"), ",") {
		if e := strings.TrimSpace(extra); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func main() {
	var (
		inject  = flag.Bool("inject", false, "fill the mailbox with the built-in realistic week")
		file    = flag.String("file", "", "inject messages and events from a JSON file instead")
		threads = flag.Bool("threads", false, "send real mail to create genuine conversation threads")
		from    = flag.String("from", "", "mailbox to send from, for --threads")
		wipe    = flag.Bool("wipe", false, "delete everything this tool created")
		confirm = flag.Bool("i-know-this-is-a-test-mailbox", false, "required safety flag")
		check   = flag.Bool("validate-only", false, "check a --file without touching the mailbox")
	)
	flag.Parse()

	// Validating a file needs no mailbox and no safety flag, so it is handled
	// before anything else - it is the fast loop when iterating on test data.
	if *check {
		if *file == "" {
			fmt.Fprintln(os.Stderr, "--validate-only needs --file")
			os.Exit(2)
		}
		f, err := loadSeedFile(*file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s is valid: %d message(s), %d event(s).\n",
			*file, len(f.Messages), len(f.Events))
		printChecklist(f)
		return
	}

	if !*inject && !*threads && !*wipe && *file == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*inject, *file, *threads, *wipe, *from, *confirm); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(1)
	}
}

func run(inject bool, file string, threads, wipe bool, from string, confirm bool) error {
	// Parse the test data before touching anything, so a malformed file fails
	// instantly rather than half-way through writing to a mailbox.
	var fromFile *SeedFile
	if file != "" {
		var err error
		if fromFile, err = loadSeedFile(file); err != nil {
			return err
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Two independent guards. Seeding writes junk into a mailbox and wiping
	// deletes from one; either against a real mailbox would be unrecoverable
	// and deeply embarrassing.
	for _, protected := range protectedList() {
		if strings.EqualFold(cfg.Mailbox, protected) {
			return fmt.Errorf(
				"refusing to touch %s - that is the production mailbox.\n"+
					"Point config.json at a developer-tenant mailbox first", cfg.Mailbox)
		}
	}
	if !confirm {
		return fmt.Errorf(
			"this writes to (or deletes from) the mailbox %s.\n"+
				"If that really is a disposable test mailbox, re-run with:\n"+
				"  --i-know-this-is-a-test-mailbox", cfg.Mailbox)
	}

	secret, err := cfg.Secret()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := graph.New(cfg.TenantID, cfg.ClientID, secret, cfg.Mailbox, cfg.Timezone)
	if err := client.Ping(ctx); err != nil {
		return err
	}

	loc := cfg.Location()
	now := time.Now().In(loc)

	if wipe {
		if err := doWipe(ctx, client); err != nil {
			return err
		}
	}
	if inject {
		if err := doInject(ctx, client, now, cfg.Timezone); err != nil {
			return err
		}
	}
	if fromFile != nil {
		if err := injectFile(ctx, client, fromFile, now, cfg.Timezone); err != nil {
			return err
		}
	}
	if threads {
		if from == "" {
			return fmt.Errorf("--threads needs --from <another mailbox in the test tenant>")
		}
		if err := doThreads(ctx, client, from); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

func doWipe(ctx context.Context, c *graph.Client) error {
	fmt.Println("Removing previously seeded items…")

	messages, err := c.SeededMessages(ctx)
	if err != nil {
		return fmt.Errorf("finding seeded messages: %w", err)
	}
	for _, m := range messages {
		if err := c.DeleteMessage(ctx, m.ID); err != nil {
			fmt.Printf("  could not delete %q: %v\n", m.Subject, err)
			continue
		}
	}
	fmt.Printf("  %d message(s) removed\n", len(messages))

	events, err := c.SeededEvents(ctx)
	if err != nil {
		return fmt.Errorf("finding seeded events: %w", err)
	}
	for _, e := range events {
		if err := c.DeleteEvent(ctx, e.ID); err != nil {
			fmt.Printf("  could not delete %q: %v\n", e.Subject, err)
			continue
		}
	}
	fmt.Printf("  %d event(s) removed\n", len(events))

	fmt.Println("\nNote: real mail created by --threads is not tagged and is not")
	fmt.Println("removed here. Delete those by hand if you need a clean slate.")
	return nil
}

// days converts a fractional day offset to a duration.
func days(d float64) time.Duration {
	return time.Duration(d * float64(24*time.Hour))
}

func doInject(ctx context.Context, c *graph.Client, now time.Time, tz string) error {
	fmt.Println("Injecting a week of foundation mail…")

	messages := []graph.SeedMessage{
		// --- Should appear as "still waiting on you", oldest first ----------
		{
			Subject: "Letter of inquiry - Mancos Valley Resource Center",
			Body: "Dear Ms. Wrinkle, please find attached our letter of inquiry requesting " +
				"$35,000 over two years to expand our food security program in Mancos.",
			FromName: "Tessa Nunn", FromAddress: "director@mancosvalleyresources.org",
			Received: now.Add(-days(11)), IsRead: true,
		},
		{
			Subject:  "Final grant report - 2025 youth mentoring award",
			Body:     "Attached is our final report on the $15,000 youth mentoring grant.",
			FromName: "Kim Baptiste", FromAddress: "kbaptiste@pinerivervalley.org",
			Received: now.Add(-days(9)), IsRead: true, Flagged: true,
		},
		{
			Subject: "Client bequest naming the Foundation",
			Body: "I'm working with a client who wants to name the Community Foundation " +
				"as a residual beneficiary. Can you send your legal name and EIN?",
			FromName: "Diane Castellano", FromAddress: "dcastellano@fourcornerslaw.com",
			Received: now.Add(-days(6)), IsRead: true,
		},
		{
			Subject:  "Interview request - regional giving trends story",
			Body:     "Would you have 30 minutes this week? I'd also love your Q2 grant numbers.",
			FromName: "Jessica Romero", FromAddress: "jromero@durangoherald.com",
			Received: now.Add(-days(4)),
		},
		{
			Subject:  "Scholarship committee - can we move the review meeting?",
			Body:     "Three of us have a conflict on the 12th. Could we meet on the 14th?",
			FromName: "Patricia Montoya", FromAddress: "pmontoya@frontier.net",
			Received: now.Add(-days(3)),
		},

		// --- Should NOT appear as waiting -----------------------------------
		{
			// Inside the grace period.
			Subject:  "Quick question about the Hoedown seating",
			Body:     "Do we need the extra tables this year?",
			FromName: "Ray Vigil", FromAddress: "rvigil@vigilconstruction.net",
			Received: now.Add(-days(0.25)),
		},
		{
			// She is only CC'd, not asked.
			Subject:  "FYI - vendor contract copy",
			Body:     "Copying you for the file.",
			FromName: "Helen Ortega", FromAddress: "boardchair@swcommunityfoundation.org",
			Received: now.Add(-days(5)), ToOverride: "someone.else@example.org", CcMailbox: true,
		},

		// --- Automated: unread but never "waiting" --------------------------
		{
			Subject:  "This week in philanthropy: DAF regulations, workforce data",
			Body:     "Treasury releases long-awaited donor advised fund guidance. Unsubscribe.",
			FromName: "Council on Foundations", FromAddress: "news@cof.org",
			Received: now.Add(-days(2)),
		},
		{
			Subject:  "Free webinar: telling your impact story with data",
			Body:     "Join us Wednesday at 2pm ET. Manage preferences.",
			FromName: "Candid Learning", FromAddress: "newsletter@candid.org",
			Received: now.Add(-days(5)),
		},
		{
			Subject:  "Disbursement report - donations processed June 2026",
			Body:     "Your June disbursement of $47,213.88 representing 214 donations.",
			FromName: "ColoradoGives", FromAddress: "noreply@coloradogives.org",
			Received: now.Add(-days(1)),
		},
		{
			Subject:  "Your Microsoft 365 subscription renews soon",
			Body:     "Your subscription for 8 users renews on August 14, 2026.",
			FromName: "Microsoft", FromAddress: "account-security-noreply@microsoft.com",
			Received: now.Add(-days(6)),
		},

		// --- Sent items, so the week-in-review numbers are non-zero ---------
		{
			Subject: "Re: Board packet for the March 19 meeting", Sent: true,
			Body:     "Thanks Charles - reviewed and approved.",
			FromName: "the Executive Director", FromAddress: c.Mailbox(),
			Received: now.Add(-days(4)),
		},
		{
			Subject: "Following up on your fund", Sent: true,
			Body:     "Lovely to see you both last week.",
			FromName: "the Executive Director", FromAddress: c.Mailbox(),
			Received: now.Add(-days(2)),
		},
		{
			Subject: "Sponsorship agreement attached", Sent: true,
			Body:     "Delighted to have Alpine Bank as presenting sponsor.",
			FromName: "the Executive Director", FromAddress: c.Mailbox(),
			Received: now.Add(-days(1)),
		},
	}

	for _, m := range messages {
		if err := c.CreateMessage(ctx, m); err != nil {
			return fmt.Errorf("creating %q: %w", m.Subject, err)
		}
		fmt.Printf("  mail: %s\n", m.Subject)
	}

	// Calendar: this week and next.
	monday := startOfWeek(now)
	events := []graph.SeedEvent{
		{Subject: "Board meeting - Q3", Start: monday.Add(days(1) + 9*time.Hour),
			End: monday.Add(days(1) + 11*time.Hour + 30*time.Minute), Attendees: 9},
		{Subject: "Coffee with the Holloways", Start: monday.Add(days(2) + 10*time.Hour),
			End: monday.Add(days(2) + 11*time.Hour), Attendees: 2},
		{Subject: "Scholarship committee review", Start: monday.Add(days(3) + 13*time.Hour),
			End: monday.Add(days(3) + 15*time.Hour), Attendees: 5},
		{Subject: "Durango Wine Experience - vendor walkthrough",
			Start: monday.Add(days(3) + 16*time.Hour),
			End:   monday.Add(days(3) + 17*time.Hour + 30*time.Minute), Attendees: 4},
		// Next week
		{Subject: "Site visit - Pagosa Youth Center", Start: monday.Add(days(8) + 10*time.Hour),
			End: monday.Add(days(8) + 12*time.Hour), Attendees: 3},
		{Subject: "Hoedown planning", Start: monday.Add(days(9) + 14*time.Hour),
			End: monday.Add(days(9) + 15*time.Hour), Attendees: 6},
	}

	for _, e := range events {
		if err := c.CreateEvent(ctx, e, tz); err != nil {
			return fmt.Errorf("creating event %q: %w", e.Subject, err)
		}
		fmt.Printf("  event: %s\n", e.Subject)
	}

	fmt.Println("\nDone. Now run:  digest --preview week.html")
	fmt.Println()
	fmt.Println("Expected in the preview:")
	fmt.Println("  - 5 items under 'Still waiting on you', Mancos Valley LOI first")
	fmt.Println("  - the Hoedown seating question absent (inside the grace period)")
	fmt.Println("  - the CC'd vendor contract absent (she was not asked)")
	fmt.Println("  - newsletters grouped under automated, never under waiting")
	fmt.Println("  - 4 meetings this week, 2 next week")
	return nil
}

func startOfWeek(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7
	d := t.AddDate(0, 0, -offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}

// doThreads creates genuinely threaded mail.
//
// Injection cannot set conversationId, so injected mail never threads and the
// reply-detection logic goes unexercised. Sending real mail through the service
// is the only way to test it properly.
func doThreads(ctx context.Context, c *graph.Client, from string) error {
	fmt.Printf("Sending real mail from %s to %s…\n", from, c.Mailbox())

	outbound := []struct{ subject, body string }{
		{"Grant agreement - signature needed",
			"Attaching the countersigned agreement. Could you send it back by Friday?"},
		{"Question about the scholarship disbursement schedule",
			"When do funds reach the student accounts office?"},
	}

	for _, m := range outbound {
		if err := c.SendAs(ctx, from, m.subject, m.body); err != nil {
			return fmt.Errorf("sending %q: %w", m.subject, err)
		}
		fmt.Printf("  sent: %s\n", m.subject)
	}

	fmt.Println("\nMail delivery takes a few seconds. Then, to test reply detection:")
	fmt.Println("  1. digest --preview before.html   -> both appear as waiting")
	fmt.Println("  2. reply to ONE of them in Outlook")
	fmt.Println("  3. digest --preview after.html    -> only the unanswered one remains")
	fmt.Println()
	fmt.Println("That is the single most important behaviour to verify by hand,")
	fmt.Println("because it is what stops the digest nagging about handled mail.")
	return nil
}
