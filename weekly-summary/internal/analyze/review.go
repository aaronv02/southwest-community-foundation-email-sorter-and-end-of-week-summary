package analyze

import (
	"sort"
	"strings"
	"time"

	"swcf/digest/internal/graph"
)

// Review is the activity summary for the week.
type Review struct {
	MeetingsHeld      int
	MeetingHours      float64
	MeetingsOrganized int
	EmailsSent        int
	PeopleWrittenTo   int
	// Conversations where she sent the last message: threads she moved along.
	ThreadsAdvanced int
	BusiestDay      string
	// A few of the week's larger commitments, for context.
	Highlights []graph.Event
}

// BuildReview summarizes what happened in the mailbox and calendar this week.
//
// A deliberate note on what this is not: Outlook cannot see accomplishment. It
// records that a meeting existed and that mail was sent, not what was decided
// or achieved, and it cannot confirm attendance - only the RSVP. Site visits,
// phone calls, grant deliberations, and anything done in another system are
// invisible here. The report is framed as activity for that reason, and the
// email template says so.
func BuildReview(
	events []graph.Event,
	sent []graph.Message,
	loc *time.Location,
	window Window,
) Review {
	r := Review{}

	perDay := make(map[time.Weekday]int)

	for _, e := range events {
		if e.IsCancelled {
			continue
		}
		start := e.Start.Time(loc)
		if start.IsZero() || start.Before(window.Start) || !start.Before(window.End) {
			continue
		}
		// All-day entries are usually holidays, travel, or out-of-office
		// markers rather than meetings, and counting their hours would wildly
		// distort the total.
		if e.IsAllDay {
			continue
		}
		// Events she declined did not happen for her.
		if e.ResponseStatus.Response == "declined" {
			continue
		}

		r.MeetingsHeld++
		r.MeetingHours += e.Duration(loc).Hours()
		if e.IsOrganizer {
			r.MeetingsOrganized++
		}
		perDay[start.Weekday()]++
	}

	correspondents := make(map[string]bool)
	lastInThread := make(map[string]bool)

	for _, m := range sent {
		when := m.SentDateTime
		if when.IsZero() {
			when = m.ReceivedDateTime
		}
		if when.Before(window.Start) || !when.Before(window.End) {
			continue
		}
		r.EmailsSent++
		for _, to := range m.ToRecipients {
			addr := strings.ToLower(to.EmailAddress.Address)
			if addr != "" {
				correspondents[addr] = true
			}
		}
		if m.ConversationID != "" {
			lastInThread[m.ConversationID] = true
		}
		perDay[when.In(loc).Weekday()]++
	}

	r.PeopleWrittenTo = len(correspondents)
	r.ThreadsAdvanced = len(lastInThread)
	r.MeetingHours = roundTo(r.MeetingHours, 1)

	if len(perDay) > 0 {
		busiest, best := time.Monday, -1
		// Iterate the weekdays in order so ties resolve to the earlier day
		// deterministically rather than by map order.
		for d := time.Sunday; d <= time.Saturday; d++ {
			if perDay[d] > best {
				busiest, best = d, perDay[d]
			}
		}
		r.BusiestDay = busiest.String()
	}

	r.Highlights = pickHighlights(events, loc, window)
	return r
}

// pickHighlights surfaces the week's most substantial meetings.
func pickHighlights(events []graph.Event, loc *time.Location, window Window) []graph.Event {
	var candidates []graph.Event
	for _, e := range events {
		if e.IsCancelled || e.IsAllDay || e.ResponseStatus.Response == "declined" {
			continue
		}
		start := e.Start.Time(loc)
		if start.IsZero() || start.Before(window.Start) || !start.Before(window.End) {
			continue
		}
		candidates = append(candidates, e)
	}

	// Weight by attendees and duration: a two-hour board meeting with nine
	// people is more of the week than a fifteen-minute one-to-one.
	sort.SliceStable(candidates, func(i, j int) bool {
		return weight(candidates[i], loc) > weight(candidates[j], loc)
	})
	if len(candidates) > 4 {
		candidates = candidates[:4]
	}
	return candidates
}

func weight(e graph.Event, loc *time.Location) float64 {
	return e.Duration(loc).Hours() + float64(e.HumanAttendeeCount())/2
}

func roundTo(v float64, places int) float64 {
	factor := 1.0
	for range places {
		factor *= 10
	}
	return float64(int(v*factor+0.5)) / factor
}
