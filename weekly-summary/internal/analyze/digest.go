// Package analyze turns raw mailbox and calendar data into the weekly digest.
//
// Everything here is a pure function of its inputs: no network, no clock
// beyond the `now` that gets passed in. That is what makes the whole report
// testable on a machine that has never seen the target mailbox.
package analyze

import (
	"sort"
	"time"

	"swcf/digest/internal/graph"
)

// Digest is the finished report, ready to render.
type Digest struct {
	Window      Window
	Mailbox     string
	GeneratedAt time.Time
	Location    *time.Location

	Waiting  []WaitingItem
	Unread   UnreadReport
	Review   Review
	Calendar CalendarGaps
	// Messages carrying an unresolved follow-up flag, whenever they arrived.
	Flagged []graph.Message
}

// Input bundles everything fetched from Graph.
type Input struct {
	Inbox  []graph.Message
	Sent   []graph.Message
	Events []graph.Event
}

// Options carries the tunables that come from configuration.
type Options struct {
	Mailbox string
	// Every address she may be reached at, including Mailbox. Role aliases and
	// shared mailboxes belong here, or mail sent to them will never register as
	// having been addressed to her.
	Addresses       []string
	Location        *time.Location
	Now             time.Time
	WaitingGrace    time.Duration
	IgnoredPatterns []string
}

// resolvedAddresses returns Addresses, defaulting to just the mailbox.
func (o Options) resolvedAddresses() []string {
	if len(o.Addresses) > 0 {
		return o.Addresses
	}
	return []string{o.Mailbox}
}

// Build assembles the digest.
func Build(in Input, window Window, opts Options) Digest {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}

	addresses := opts.resolvedAddresses()
	waiting := Waiting(in.Inbox, in.Sent, addresses, opts.WaitingGrace,
		opts.Now, opts.IgnoredPatterns)

	d := Digest{
		Window:      window,
		Mailbox:     opts.Mailbox,
		GeneratedAt: opts.Now.In(loc),
		Location:    loc,
		Waiting:     waiting,
		Unread:      Unread(in.Inbox, addresses, opts.Now, opts.IgnoredPatterns),
		Review:      BuildReview(in.Events, in.Sent, loc, window),
		Calendar:    BuildCalendarGaps(in.Events, loc, window),
		Flagged:     openFlags(in.Inbox, waiting),
	}
	return d
}

// openFlags collects messages she flagged and never cleared.
//
// A flag is her own explicit "come back to this", which makes it a stronger
// signal than anything the tool infers - so these are reported regardless of
// age, sender, or whether she was on the To line.
//
// Anything already listed as waiting is omitted: the same item appearing twice
// in one email reads as a bug and pads a list whose whole value is that it is
// short and true.
func openFlags(inbox []graph.Message, waiting []WaitingItem) []graph.Message {
	alreadyListed := make(map[string]bool, len(waiting)*2)
	for _, w := range waiting {
		alreadyListed["id:"+w.Message.ID] = true
		if w.Message.ConversationID != "" {
			alreadyListed["conv:"+w.Message.ConversationID] = true
		}
	}

	var out []graph.Message
	for _, m := range inbox {
		if !m.IsFlagged() {
			continue
		}
		if alreadyListed["id:"+m.ID] ||
			(m.ConversationID != "" && alreadyListed["conv:"+m.ConversationID]) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ReceivedDateTime.Before(out[j].ReceivedDateTime)
	})
	return out
}

// HasAnythingToSay reports whether the digest contains anything actionable.
//
// Used to soften the subject line on a genuinely quiet week rather than
// announcing "0 items waiting" as though it were a finding.
func (d Digest) HasAnythingToSay() bool {
	return len(d.Waiting) > 0 ||
		len(d.Flagged) > 0 ||
		d.Unread.Total > 0 ||
		len(d.Calendar.NextWeekUnanswered) > 0
}

// Headline is the one-sentence summary used as the email subject.
func (d Digest) Headline() string {
	switch {
	case len(d.Waiting) == 1:
		return "1 email is waiting on you"
	case len(d.Waiting) > 1:
		return itoa(len(d.Waiting)) + " emails are waiting on you"
	case d.Unread.StaleCount > 0:
		return itoa(d.Unread.StaleCount) + " unread from last week or earlier"
	case d.Unread.Total > 0:
		return itoa(d.Unread.Total) + " unread"
	default:
		return "nothing waiting - inbox is clear"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
