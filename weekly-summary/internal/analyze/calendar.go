package analyze

import (
	"sort"
	"time"

	"swcf/digest/internal/graph"
)

// CalendarGaps covers RSVPs left hanging and what is coming next.
type CalendarGaps struct {
	// Invitations in the reporting week she never responded to.
	Unanswered []graph.Event
	// Meetings she declined, for the record.
	Declined []graph.Event
	// Next week's schedule.
	NextWeek []graph.Event
	// Next week's invitations still awaiting an RSVP - the actionable ones.
	NextWeekUnanswered []graph.Event
	// Hours already committed next week.
	NextWeekHours float64
}

// BuildCalendarGaps reports RSVP debt and the week ahead.
//
// Unanswered invitations are the calendar equivalent of unanswered mail: an
// organizer is waiting on her, and unlike email there is no thread to remind
// anyone. Next week's are separated out because those are the ones she can
// still do something about.
func BuildCalendarGaps(events []graph.Event, loc *time.Location, window Window) CalendarGaps {
	g := CalendarGaps{}

	for _, e := range events {
		if e.IsCancelled {
			continue
		}
		start := e.Start.Time(loc)
		if start.IsZero() {
			continue
		}

		inWindow := !start.Before(window.Start) && start.Before(window.End)
		inNext := !start.Before(window.NextStart) && start.Before(window.NextEnd)

		switch {
		case inWindow:
			// "none" shows up on events with no invitation semantics, such as
			// appointments she created for herself; only a real un-answered
			// invite counts.
			if e.ResponseStatus.Response == "notResponded" && !e.IsOrganizer {
				g.Unanswered = append(g.Unanswered, e)
			}
			if e.ResponseStatus.Response == "declined" {
				g.Declined = append(g.Declined, e)
			}

		case inNext:
			g.NextWeek = append(g.NextWeek, e)
			if !e.IsAllDay && e.ResponseStatus.Response != "declined" {
				g.NextWeekHours += e.Duration(loc).Hours()
			}
			if e.ResponseStatus.Response == "notResponded" && !e.IsOrganizer {
				g.NextWeekUnanswered = append(g.NextWeekUnanswered, e)
			}
		}
	}

	byStart := func(list []graph.Event) {
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].Start.Time(loc).Before(list[j].Start.Time(loc))
		})
	}
	byStart(g.Unanswered)
	byStart(g.Declined)
	byStart(g.NextWeek)
	byStart(g.NextWeekUnanswered)

	g.NextWeekHours = roundTo(g.NextWeekHours, 1)
	return g
}
