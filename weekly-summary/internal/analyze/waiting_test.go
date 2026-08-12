package analyze

import (
	"testing"
	"time"

	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
)

const mailbox = "director@example.org"

func inbound(from, name, subject, convID string, received time.Time, to ...string) graph.Message {
	if len(to) == 0 {
		to = []string{mailbox}
	}
	recipients := make([]graph.Recipient, 0, len(to))
	for _, addr := range to {
		recipients = append(recipients, graph.Recipient{
			EmailAddress: graph.EmailAddress{Address: addr},
		})
	}
	return graph.Message{
		ID:               subject,
		ConversationID:   convID,
		Subject:          subject,
		ReceivedDateTime: received,
		From:             &graph.Recipient{EmailAddress: graph.EmailAddress{Address: from, Name: name}},
		ToRecipients:     recipients,
	}
}

func outbound(convID string, sent time.Time) graph.Message {
	return graph.Message{
		ID:             "sent-" + convID,
		ConversationID: convID,
		SentDateTime:   sent,
		From:           &graph.Recipient{EmailAddress: graph.EmailAddress{Address: mailbox}},
	}
}

func waitingSubjects(items []WaitingItem) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it.Message.Subject] = true
	}
	return out
}

func TestWaitingFindsUnansweredDirectMail(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	inbox := []graph.Message{
		inbound("tessa@mancosvalleyresources.org", "Tessa Nunn",
			"Letter of inquiry", "c1", now.Add(-5*24*time.Hour)),
	}

	got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, config.DefaultIgnoredSenders)

	if len(got) != 1 {
		t.Fatalf("expected 1 waiting item, got %d", len(got))
	}
	if got[0].AgeDays() != 5 {
		t.Errorf("age = %d days, want 5", got[0].AgeDays())
	}
}

func TestWaitingIgnoresAnsweredThreads(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	received := now.Add(-5 * 24 * time.Hour)

	inbox := []graph.Message{inbound("a@b.org", "A", "Answered", "c1", received)}
	sent := []graph.Message{outbound("c1", received.Add(2*time.Hour))}

	if got := Waiting(inbox, sent, []string{mailbox}, 48*time.Hour, now, nil); len(got) != 0 {
		t.Errorf("a replied-to thread must not be reported, got %d", len(got))
	}
}

// A reply that predates the message does not answer it - this catches the
// off-by-one of matching on conversation alone.
func TestWaitingIgnoresRepliesSentBeforeTheMessage(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	received := now.Add(-3 * 24 * time.Hour)

	inbox := []graph.Message{inbound("a@b.org", "A", "Later message", "c1", received)}
	sent := []graph.Message{outbound("c1", received.Add(-24*time.Hour))}

	if got := Waiting(inbox, sent, []string{mailbox}, 48*time.Hour, now, nil); len(got) != 1 {
		t.Errorf("an earlier reply does not answer a later message, got %d items", len(got))
	}
}

func TestWaitingIgnoresCarbonCopies(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	// Addressed to someone else; she is only CC'd, so the To line omits her.
	msg := inbound("a@b.org", "A", "FYI thread", "c1",
		now.Add(-5*24*time.Hour), "someone.else@example.org")
	msg.CcRecipients = []graph.Recipient{
		{EmailAddress: graph.EmailAddress{Address: mailbox}},
	}

	if got := Waiting([]graph.Message{msg}, nil, []string{mailbox}, 48*time.Hour, now, nil); len(got) != 0 {
		t.Error("being CC'd is being kept informed, not being asked")
	}
}

func TestWaitingRespectsTheGracePeriod(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	inbox := []graph.Message{
		inbound("a@b.org", "A", "This morning", "c1", now.Add(-3*time.Hour)),
		inbound("c@d.org", "C", "Last Tuesday", "c2", now.Add(-9*24*time.Hour)),
	}

	got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, nil)

	subjects := waitingSubjects(got)
	if subjects["This morning"] {
		t.Error("mail from this morning is not a failure to respond")
	}
	if !subjects["Last Tuesday"] {
		t.Error("mail from last week should be reported")
	}
}

func TestWaitingSkipsAutomatedSenders(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	old := now.Add(-6 * 24 * time.Hour)

	inbox := []graph.Message{
		inbound("noreply@coloradogives.org", "ColoradoGives", "Disbursement", "c1", old),
		inbound("news@cof.org", "Council on Foundations", "Weekly", "c2", old),
		inbound("newsletter@candid.org", "Candid", "Webinar", "c3", old),
		inbound("real.person@example.org", "A Person", "Actual question", "c4", old),
	}

	got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, config.DefaultIgnoredSenders)

	if len(got) != 1 {
		t.Fatalf("expected only the human sender, got %d: %v", len(got), waitingSubjects(got))
	}
	if got[0].Message.Subject != "Actual question" {
		t.Errorf("kept the wrong message: %s", got[0].Message.Subject)
	}
}

// A long unanswered thread is one thing to do, not one per message.
func TestWaitingCollapsesThreadsToOneEntry(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	base := now.Add(-6 * 24 * time.Hour)

	inbox := []graph.Message{
		inbound("a@b.org", "A", "Following up again", "c1", base.Add(48*time.Hour)),
		inbound("a@b.org", "A", "Following up", "c1", base.Add(24*time.Hour)),
		inbound("a@b.org", "A", "Original ask", "c1", base),
	}

	got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, nil)

	if len(got) != 1 {
		t.Fatalf("a thread should produce one entry, got %d", len(got))
	}
	if got[0].Message.Subject != "Original ask" {
		t.Errorf("should surface the oldest message in the thread, got %q",
			got[0].Message.Subject)
	}
}

func TestWaitingIsSortedOldestFirst(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	inbox := []graph.Message{
		inbound("a@b.org", "A", "Recent", "c1", now.Add(-3*24*time.Hour)),
		inbound("c@d.org", "C", "Ancient", "c2", now.Add(-18*24*time.Hour)),
		inbound("e@f.org", "E", "Middle", "c3", now.Add(-9*24*time.Hour)),
	}

	got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, nil)

	want := []string{"Ancient", "Middle", "Recent"}
	for i, subject := range want {
		if got[i].Message.Subject != subject {
			t.Errorf("position %d = %q, want %q", i, got[i].Message.Subject, subject)
		}
	}
}

func TestWaitingIgnoresMailFromHerself(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	inbox := []graph.Message{
		inbound(mailbox, "the director", "Note to self", "c1", now.Add(-6*24*time.Hour)),
	}

	if got := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, nil); len(got) != 0 {
		t.Error("her own mail should not appear as waiting on her")
	}
}

// Role aliases. At a small nonprofit one person is often reachable at several
// addresses, and mail sent to an alias the digest doesn't know about silently
// vanishes from the waiting list - a short list with no error to explain it.
func TestWaitingRecognizesRoleAliases(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	old := now.Add(-5 * 24 * time.Hour)

	inbox := []graph.Message{
		inbound("a@b.org", "A", "To her directly", "c1", old),
		inbound("c@d.org", "C", "To the info alias", "c2", old, "info@swcommunityfoundation.org"),
		inbound("e@f.org", "E", "To the grants alias", "c3", old, "grants@swcommunityfoundation.org"),
		inbound("g@h.org", "G", "To a stranger", "c4", old, "someone.else@elsewhere.org"),
	}

	// Primary address only: the alias mail is invisible.
	narrow := Waiting(inbox, nil, []string{mailbox}, 48*time.Hour, now, nil)
	if len(narrow) != 1 {
		t.Fatalf("with one address, expected 1 waiting item, got %d", len(narrow))
	}

	// With the aliases configured, all three reach her.
	wide := Waiting(inbox, nil, []string{
		mailbox,
		"info@swcommunityfoundation.org",
		"grants@swcommunityfoundation.org",
	}, 48*time.Hour, now, nil)

	if len(wide) != 3 {
		t.Fatalf("with aliases, expected 3 waiting items, got %d: %v",
			len(wide), waitingSubjects(wide))
	}
	if waitingSubjects(wide)["To a stranger"] {
		t.Error("mail addressed to an unrelated person must never count")
	}
}

// Mail she sent from any of her own addresses is not waiting on her.
func TestWaitingIgnoresMailFromAnyOfHerOwnAddresses(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	addresses := []string{mailbox, "info@swcommunityfoundation.org"}

	inbox := []graph.Message{
		inbound("INFO@swcommunityfoundation.org", "the director", "Sent from the alias", "c1",
			now.Add(-6*24*time.Hour)),
	}

	if got := Waiting(inbox, nil, addresses, 48*time.Hour, now, nil); len(got) != 0 {
		t.Errorf("her own alias should not appear as waiting on her, got %v",
			waitingSubjects(got))
	}
}
