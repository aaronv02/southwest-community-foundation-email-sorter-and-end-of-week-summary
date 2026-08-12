package report_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swcf/digest/internal/analyze"
	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
	"swcf/digest/internal/report"
)

const mailbox = "director@example.org"

// sampleDigest builds a realistic week for the foundation.
//
// This exists because the target mailbox is unavailable to us: it is the only
// way to see what the email actually looks like before it reaches a real inbox.
// The scenario is deliberately messy - unanswered grant mail, a flag left open,
// unread newsletters - so the rendering is exercised rather than an empty
// happy path.
func sampleDigest(t *testing.T) analyze.Digest {
	t.Helper()

	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("timezone: %v", err)
	}
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, loc)
	window := analyze.ResolveWindow(now, loc, "")

	inbound := func(from, name, subject string, daysAgo float64, read bool) graph.Message {
		return graph.Message{
			ID:               subject,
			ConversationID:   subject,
			Subject:          subject,
			ReceivedDateTime: now.Add(-time.Duration(daysAgo * float64(24*time.Hour))),
			IsRead:           read,
			From:             &graph.Recipient{EmailAddress: graph.EmailAddress{Address: from, Name: name}},
			ToRecipients: []graph.Recipient{
				{EmailAddress: graph.EmailAddress{Address: mailbox}},
			},
		}
	}

	inbox := []graph.Message{
		inbound("director@mancosvalleyresources.org", "Tessa Nunn",
			"Letter of inquiry - Mancos Valley Resource Center", 11, true),
		inbound("dcastellano@fourcornerslaw.com", "Diane Castellano",
			"Client bequest naming the Foundation", 6, true),
		inbound("jromero@durangoherald.com", "Jessica Romero",
			"Interview request - regional giving trends story", 4, false),
		inbound("pmontoya@frontier.net", "Patricia Montoya",
			"Scholarship committee - can we move the review meeting?", 3, false),
		inbound("news@cof.org", "Council on Foundations",
			"This week in philanthropy: DAF regulations", 2, false),
		inbound("newsletter@candid.org", "Candid Learning",
			"Free webinar: telling your impact story with data", 5, false),
		inbound("noreply@coloradogives.org", "ColoradoGives",
			"Disbursement report - donations processed June 2026", 1, false),
		// Answered, so it must not appear as waiting.
		inbound("cwexler@wexlercpa.com", "Charles Wexler",
			"Board packet for the March 19 meeting", 5, true),
	}

	flagged := inbound("kbaptiste@pinerivervalley.org", "Kim Baptiste",
		"Final grant report - 2025 youth mentoring award", 9, true)
	flagged.Flag = &graph.FollowupFlag{FlagStatus: "flagged"}
	flagged.HasAttachments = true
	inbox = append(inbox, flagged)

	sent := []graph.Message{
		{
			ID:             "s1",
			ConversationID: "Board packet for the March 19 meeting",
			SentDateTime:   now.Add(-4 * 24 * time.Hour),
			ToRecipients: []graph.Recipient{
				{EmailAddress: graph.EmailAddress{Address: "cwexler@wexlercpa.com"}},
			},
		},
		{
			ID:             "s2",
			ConversationID: "donor-thread",
			SentDateTime:   now.Add(-2 * 24 * time.Hour),
			ToRecipients: []graph.Recipient{
				{EmailAddress: graph.EmailAddress{Address: "margaret.holloway@gmail.com"}},
				{EmailAddress: graph.EmailAddress{Address: "jwhitaker@whitakerranch.net"}},
			},
		},
		{
			ID:             "s3",
			ConversationID: "events-thread",
			SentDateTime:   now.Add(-1 * 24 * time.Hour),
			ToRecipients: []graph.Recipient{
				{EmailAddress: graph.EmailAddress{Address: "sponsorships@alpinebankco.com"}},
			},
		},
	}

	event := func(subject string, start time.Time, hours float64, organizer bool,
		response string, attendees int) graph.Event {
		end := start.Add(time.Duration(hours * float64(time.Hour)))
		list := make([]graph.Attendee, attendees)
		for i := range list {
			list[i] = graph.Attendee{Type: "required"}
		}
		return graph.Event{
			ID:             subject,
			Subject:        subject,
			Start:          graph.DateTimeTimeZone{DateTime: start.Format("2006-01-02T15:04:05.0000000")},
			End:            graph.DateTimeTimeZone{DateTime: end.Format("2006-01-02T15:04:05.0000000")},
			IsOrganizer:    organizer,
			ResponseStatus: graph.ResponseStatus{Response: response},
			Attendees:      list,
			Organizer: &graph.Recipient{
				EmailAddress: graph.EmailAddress{Name: "Helen Ortega"},
			},
		}
	}

	events := []graph.Event{
		event("Board meeting - Q3", time.Date(2026, 7, 28, 9, 0, 0, 0, loc), 2.5, true, "organizer", 9),
		event("Coffee with the Holloways", time.Date(2026, 7, 29, 10, 0, 0, 0, loc), 1, false, "accepted", 2),
		event("Scholarship committee review", time.Date(2026, 7, 30, 13, 0, 0, 0, loc), 2, true, "organizer", 5),
		event("Durango Wine Experience - vendor walkthrough",
			time.Date(2026, 7, 30, 16, 0, 0, 0, loc), 1.5, false, "accepted", 4),
		event("Regional funders call", time.Date(2026, 7, 29, 15, 0, 0, 0, loc), 1, false, "notResponded", 12),
		// Next week
		event("Site visit - Pagosa Youth Center", time.Date(2026, 8, 4, 10, 0, 0, 0, loc), 2, false, "accepted", 3),
		event("Hoedown planning", time.Date(2026, 8, 5, 14, 0, 0, 0, loc), 1, true, "organizer", 6),
		event("Philanthropy Colorado convening prep",
			time.Date(2026, 8, 6, 9, 0, 0, 0, loc), 1.5, false, "notResponded", 8),
	}

	return analyze.Build(
		analyze.Input{Inbox: inbox, Sent: sent, Events: events},
		window,
		analyze.Options{
			Mailbox:         mailbox,
			Location:        loc,
			Now:             now,
			WaitingGrace:    48 * time.Hour,
			IgnoredPatterns: config.DefaultIgnoredSenders,
		},
	)
}

func TestRenderProducesCompleteEmail(t *testing.T) {
	d := sampleDigest(t)

	html, err := report.Render(d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	mustContain := []string{
		"Still waiting on you",
		"You didn't open these",
		"What your week looked like",
		"Calendar",
		"Letter of inquiry",               // oldest unanswered, should lead
		"Final grant report",              // the open flag
		"Jessica Romero",                  // unread from a person
		"Council on Foundations",          // bulk unread, grouped separately
		"Philanthropy Colorado convening", // next week's unanswered invite
		mailbox,
	}
	for _, want := range mustContain {
		if !strings.Contains(html, want) {
			t.Errorf("rendered email is missing %q", want)
		}
	}

	// Word's rendering engine ignores these, so they must not be relied on.
	for _, forbidden := range []string{"display:flex", "display:grid", "position:absolute"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("template uses %q, which Outlook on Windows will not render", forbidden)
		}
	}
}

func TestWaitingSectionExcludesAnsweredAndBulk(t *testing.T) {
	d := sampleDigest(t)

	var subjects []string
	for _, w := range d.Waiting {
		subjects = append(subjects, w.Message.Subject)
	}
	joined := strings.Join(subjects, " | ")

	if !strings.Contains(joined, "Letter of inquiry") {
		t.Errorf("expected the unanswered LOI in waiting, got: %s", joined)
	}
	if strings.Contains(joined, "Board packet") {
		t.Error("a thread she replied to must not appear as waiting")
	}
	if strings.Contains(joined, "Disbursement report") {
		t.Error("automated mail must not appear as waiting")
	}
	if strings.Contains(joined, "This week in philanthropy") {
		t.Error("a newsletter must not appear as waiting")
	}

	// Oldest first.
	if len(d.Waiting) > 1 {
		first, second := d.Waiting[0], d.Waiting[1]
		if first.Message.ReceivedDateTime.After(second.Message.ReceivedDateTime) {
			t.Error("waiting list is not oldest-first")
		}
	}
}

func TestSubjectLeadsWithTheActionableNumber(t *testing.T) {
	d := sampleDigest(t)

	subject := report.Subject(d)
	if !strings.Contains(subject, "waiting on you") {
		t.Errorf("subject should lead with what needs action, got %q", subject)
	}
	if !strings.Contains(subject, "Jul") {
		t.Errorf("subject should name the week, got %q", subject)
	}
}

// Writes a viewable sample to dist/. Not an assertion so much as the only way
// to eyeball the output without a mailbox: open the file in a browser.
func TestWriteSampleForInspection(t *testing.T) {
	d := sampleDigest(t)

	html, err := report.Render(d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	out := filepath.Join("..", "..", "dist", "sample-digest.html")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("creating dist: %v", err)
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatalf("writing sample: %v", err)
	}

	abs, _ := filepath.Abs(out)
	t.Logf("sample digest written to %s", abs)
	t.Logf("subject: %s", report.Subject(d))
}
