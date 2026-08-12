package analyze

import (
	"fmt"
	"time"
)

// Window is the stretch of time a digest reports on.
type Window struct {
	// ISO label such as "2026-W31". Used as the idempotency key so a
	// catch-up run cannot send the same week twice.
	Label string
	// Inclusive start and exclusive end, in the mailbox's local timezone.
	Start time.Time
	End   time.Time
	// The following week, for the look-ahead section.
	NextStart time.Time
	NextEnd   time.Time
	// True when this run is covering a week whose scheduled slot was missed.
	CatchUp bool
}

// ResolveWindow decides which week to report on.
//
// This is the fix for the central weakness of running on a laptop: if the
// machine is asleep at 4pm Friday, the scheduled task fires whenever it next
// wakes, which might be Monday. Naively reporting "the current week" would then
// produce an almost-empty digest for a week that has barely started, and the
// week that actually mattered would never be reported at all.
//
// So the rule is: Friday through Sunday reports the current week. Monday
// through Thursday reports the previous week, but only if that week was never
// sent - otherwise it reports the current week as usual.
func ResolveWindow(now time.Time, loc *time.Location, lastSentWeek string) Window {
	now = now.In(loc)
	currentStart := isoWeekStart(now, loc)

	target := currentStart
	catchUp := false

	if now.Weekday() >= time.Monday && now.Weekday() <= time.Thursday {
		previousStart := currentStart.AddDate(0, 0, -7)
		if isoLabel(previousStart) != lastSentWeek {
			target = previousStart
			catchUp = true
		}
	}

	end := target.AddDate(0, 0, 7)
	if now.Before(end) {
		// Never claim to cover time that has not happened yet.
		end = now
	}

	nextStart := target.AddDate(0, 0, 7)
	return Window{
		Label:     isoLabel(target),
		Start:     target,
		End:       end,
		NextStart: nextStart,
		NextEnd:   nextStart.AddDate(0, 0, 7),
		CatchUp:   catchUp,
	}
}

// isoWeekStart returns Monday 00:00 local of the ISO week containing t.
func isoWeekStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	// Go's Weekday puts Sunday at 0; ISO weeks start on Monday.
	offset := (int(t.Weekday()) + 6) % 7
	day := t.AddDate(0, 0, -offset)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

func isoLabel(weekStart time.Time) string {
	year, week := weekStart.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// Describe renders the window for a human, e.g. "Mon 27 Jul – Fri 31 Jul".
func (w Window) Describe() string {
	end := w.End
	if !end.After(w.Start) {
		end = w.Start
	}
	// The end is exclusive; show the last day actually covered.
	last := end.Add(-time.Second)
	if last.Before(w.Start) {
		last = w.Start
	}
	if w.Start.Month() == last.Month() {
		return fmt.Sprintf("%s – %s", w.Start.Format("Mon 2"), last.Format("Mon 2 Jan"))
	}
	return fmt.Sprintf("%s – %s", w.Start.Format("Mon 2 Jan"), last.Format("Mon 2 Jan"))
}
