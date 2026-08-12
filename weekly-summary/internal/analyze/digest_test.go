package analyze

import (
	"testing"
	"time"

	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
)

func unreadMsg(from, name, subject string, received time.Time) graph.Message {
	m := inbound(from, name, subject, subject, received)
	m.IsRead = false
	return m
}

func TestUnreadSeparatesPeopleFromBulk(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * 24 * time.Hour)

	inbox := []graph.Message{
		unreadMsg("tessa@mancosvalleyresources.org", "Tessa Nunn", "LOI question", recent),
		unreadMsg("tessa@mancosvalleyresources.org", "Tessa Nunn", "One more thing", recent),
		unreadMsg("news@cof.org", "Council on Foundations", "Weekly digest", recent),
		unreadMsg("newsletter@candid.org", "Candid", "Webinar", recent),
	}
	// A read message must not be counted.
	read := unreadMsg("someone@example.org", "Someone", "Already read", recent)
	read.IsRead = true
	inbox = append(inbox, read)

	got := Unread(inbox, []string{mailbox}, now, config.DefaultIgnoredSenders)

	if got.Total != 4 {
		t.Errorf("total = %d, want 4 (read mail excluded)", got.Total)
	}
	if len(got.People) != 1 {
		t.Fatalf("people groups = %d, want 1", len(got.People))
	}
	if got.People[0].Count != 2 {
		t.Errorf("Tessa's count = %d, want 2", got.People[0].Count)
	}
	if len(got.Automated) != 2 {
		t.Errorf("automated groups = %d, want 2", len(got.Automated))
	}
}

func TestUnreadCountsStaleSeparately(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)

	inbox := []graph.Message{
		unreadMsg("a@b.org", "A", "Yesterday", now.Add(-24*time.Hour)),
		unreadMsg("c@d.org", "C", "Three weeks ago", now.Add(-21*24*time.Hour)),
	}

	got := Unread(inbox, []string{mailbox}, now, nil)

	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	if got.StaleCount != 1 {
		t.Errorf("stale = %d, want 1 (only the three-week-old one)", got.StaleCount)
	}
}

func event(subject string, start time.Time, hours float64, organizer bool, response string) graph.Event {
	end := start.Add(time.Duration(hours * float64(time.Hour)))
	return graph.Event{
		ID:             subject,
		Subject:        subject,
		Start:          graph.DateTimeTimeZone{DateTime: start.Format("2006-01-02T15:04:05.0000000")},
		End:            graph.DateTimeTimeZone{DateTime: end.Format("2006-01-02T15:04:05.0000000")},
		IsOrganizer:    organizer,
		ResponseStatus: graph.ResponseStatus{Response: response},
	}
}

func testWindow(t *testing.T) (Window, *time.Location) {
	t.Helper()
	loc := denver(t)
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, loc)
	return ResolveWindow(now, loc, ""), loc
}

func TestReviewCountsMeetingsAndHours(t *testing.T) {
	w, loc := testWindow(t)
	mon := time.Date(2026, 7, 27, 9, 0, 0, 0, loc)

	events := []graph.Event{
		event("Board meeting", mon, 2, true, "organizer"),
		event("Donor coffee", mon.AddDate(0, 0, 1), 1, false, "accepted"),
	}

	r := BuildReview(events, nil, loc, w)

	if r.MeetingsHeld != 2 {
		t.Errorf("meetings = %d, want 2", r.MeetingsHeld)
	}
	if r.MeetingHours != 3 {
		t.Errorf("hours = %v, want 3", r.MeetingHours)
	}
	if r.MeetingsOrganized != 1 {
		t.Errorf("organized = %d, want 1", r.MeetingsOrganized)
	}
}

func TestReviewExcludesDeclinedCancelledAndAllDay(t *testing.T) {
	w, loc := testWindow(t)
	mon := time.Date(2026, 7, 27, 9, 0, 0, 0, loc)

	declined := event("Declined thing", mon, 1, false, "declined")
	cancelled := event("Cancelled thing", mon, 1, false, "accepted")
	cancelled.IsCancelled = true
	// An all-day entry is usually travel or a holiday; counting eight hours of
	// "meeting time" for it would make the numbers meaningless.
	allDay := event("Out of office", mon, 24, false, "accepted")
	allDay.IsAllDay = true

	r := BuildReview([]graph.Event{declined, cancelled, allDay}, nil, loc, w)

	if r.MeetingsHeld != 0 {
		t.Errorf("meetings = %d, want 0", r.MeetingsHeld)
	}
	if r.MeetingHours != 0 {
		t.Errorf("hours = %v, want 0", r.MeetingHours)
	}
}

func TestReviewCountsSentMailWithinTheWindowOnly(t *testing.T) {
	w, loc := testWindow(t)

	inside := outbound("c1", time.Date(2026, 7, 28, 10, 0, 0, 0, loc))
	inside.ToRecipients = []graph.Recipient{
		{EmailAddress: graph.EmailAddress{Address: "a@b.org"}},
		{EmailAddress: graph.EmailAddress{Address: "c@d.org"}},
	}
	// Sent the previous week: fetched so it can silence old inbound mail, but
	// it is not part of this week's activity.
	before := outbound("c2", time.Date(2026, 7, 20, 10, 0, 0, 0, loc))

	r := BuildReview(nil, []graph.Message{inside, before}, loc, w)

	if r.EmailsSent != 1 {
		t.Errorf("sent = %d, want 1", r.EmailsSent)
	}
	if r.PeopleWrittenTo != 2 {
		t.Errorf("correspondents = %d, want 2", r.PeopleWrittenTo)
	}
	if r.ThreadsAdvanced != 1 {
		t.Errorf("threads = %d, want 1", r.ThreadsAdvanced)
	}
}

func TestCalendarFindsUnansweredInvitations(t *testing.T) {
	w, loc := testWindow(t)

	thisWeek := event("Unanswered this week",
		time.Date(2026, 7, 29, 10, 0, 0, 0, loc), 1, false, "notResponded")
	nextWeek := event("Unanswered next week",
		time.Date(2026, 8, 5, 10, 0, 0, 0, loc), 1, false, "notResponded")
	// Her own appointment: no invitation, so nothing to respond to.
	ownEvent := event("Focus block",
		time.Date(2026, 7, 29, 14, 0, 0, 0, loc), 2, true, "organizer")

	g := BuildCalendarGaps([]graph.Event{thisWeek, nextWeek, ownEvent}, loc, w)

	if len(g.Unanswered) != 1 || g.Unanswered[0].Subject != "Unanswered this week" {
		t.Errorf("this week's unanswered = %v", g.Unanswered)
	}
	if len(g.NextWeekUnanswered) != 1 || g.NextWeekUnanswered[0].Subject != "Unanswered next week" {
		t.Errorf("next week's unanswered = %v", g.NextWeekUnanswered)
	}
	if len(g.NextWeek) != 1 {
		t.Errorf("next week events = %d, want 1", len(g.NextWeek))
	}
}

func TestFlaggedMessagesAreCollected(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)

	flagged := inbound("a@b.org", "A", "Come back to this", "c1", now.Add(-4*24*time.Hour))
	flagged.Flag = &graph.FollowupFlag{FlagStatus: "flagged"}

	done := inbound("c@d.org", "C", "Handled", "c2", now.Add(-4*24*time.Hour))
	done.Flag = &graph.FollowupFlag{FlagStatus: "complete"}

	plain := inbound("e@f.org", "E", "Unflagged", "c3", now.Add(-4*24*time.Hour))

	got := openFlags([]graph.Message{flagged, done, plain}, nil)

	if len(got) != 1 || got[0].Subject != "Come back to this" {
		t.Errorf("open flags = %v, want only the active flag", got)
	}
}

// The same item showing up under two headings reads as a bug and pads a list
// whose entire value is being short and true.
func TestFlaggedSectionDoesNotRepeatWaitingItems(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)

	msg := inbound("a@b.org", "A", "Both flagged and unanswered", "c1",
		now.Add(-6*24*time.Hour))
	msg.Flag = &graph.FollowupFlag{FlagStatus: "flagged"}

	waiting := Waiting([]graph.Message{msg}, nil, []string{mailbox}, 48*time.Hour, now, nil)
	if len(waiting) != 1 {
		t.Fatalf("setup: expected the message to be waiting, got %d", len(waiting))
	}

	if got := openFlags([]graph.Message{msg}, waiting); len(got) != 0 {
		t.Errorf("flagged section repeated an item already listed as waiting: %v", got)
	}
}

func TestHeadlineLeadsWithWhatMatters(t *testing.T) {
	cases := []struct {
		name string
		d    Digest
		want string
	}{
		{"waiting wins", Digest{Waiting: make([]WaitingItem, 3)}, "3 emails are waiting on you"},
		{"singular", Digest{Waiting: make([]WaitingItem, 1)}, "1 email is waiting on you"},
		{"stale unread", Digest{Unread: UnreadReport{Total: 9, StaleCount: 4}},
			"4 unread from last week or earlier"},
		{"plain unread", Digest{Unread: UnreadReport{Total: 5}}, "5 unread"},
		{"all clear", Digest{}, "nothing waiting - inbox is clear"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.Headline(); got != tc.want {
				t.Errorf("Headline() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildProducesACoherentDigest(t *testing.T) {
	w, loc := testWindow(t)
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, loc)

	in := Input{
		Inbox: []graph.Message{
			inbound("tessa@mancosvalleyresources.org", "Tessa Nunn", "LOI", "c1",
				now.Add(-5*24*time.Hour)),
		},
		Sent:   []graph.Message{outbound("c9", now.Add(-2*24*time.Hour))},
		Events: []graph.Event{event("Board", time.Date(2026, 7, 28, 9, 0, 0, 0, loc), 2, true, "organizer")},
	}

	d := Build(in, w, Options{
		Mailbox:         mailbox,
		Location:        loc,
		Now:             now,
		WaitingGrace:    48 * time.Hour,
		IgnoredPatterns: config.DefaultIgnoredSenders,
	})

	if len(d.Waiting) != 1 {
		t.Errorf("waiting = %d, want 1", len(d.Waiting))
	}
	if d.Review.MeetingsHeld != 1 {
		t.Errorf("meetings = %d, want 1", d.Review.MeetingsHeld)
	}
	if !d.HasAnythingToSay() {
		t.Error("a digest with an unanswered email has something to say")
	}
}
