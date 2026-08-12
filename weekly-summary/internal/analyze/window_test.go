package analyze

import (
	"testing"
	"time"
)

func denver(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("loading timezone: %v", err)
	}
	return loc
}

func TestFridayReportsCurrentWeek(t *testing.T) {
	loc := denver(t)
	// Friday 31 July 2026, 4pm - the scheduled slot.
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, loc)

	w := ResolveWindow(now, loc, "")

	if w.CatchUp {
		t.Error("a run at the scheduled time is not a catch-up")
	}
	if got, want := w.Start.Format("2006-01-02"), "2026-07-27"; got != want {
		t.Errorf("week starts %s, want Monday %s", got, want)
	}
	if !w.End.Equal(now) {
		t.Errorf("window should end at the run time, got %v", w.End)
	}
	if w.Label != "2026-W31" {
		t.Errorf("label = %s, want 2026-W31", w.Label)
	}
}

// The whole reason a laptop-hosted job needs care: if the machine was off at
// 4pm Friday, the task fires on Monday. Reporting "this week" then would
// summarize a week that is six hours old and silently lose the week that
// mattered.
func TestMondayCatchUpReportsTheMissedWeek(t *testing.T) {
	loc := denver(t)
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, loc) // Monday morning

	w := ResolveWindow(now, loc, "" /* nothing ever sent */)

	if !w.CatchUp {
		t.Error("expected a catch-up run")
	}
	if got, want := w.Start.Format("2006-01-02"), "2026-07-27"; got != want {
		t.Errorf("catch-up should cover the missed week starting %s, got %s", want, got)
	}
	if got, want := w.End.Format("2006-01-02"), "2026-08-03"; got != want {
		t.Errorf("catch-up should cover the full missed week ending %s, got %s", want, got)
	}
	if w.Label != "2026-W31" {
		t.Errorf("label = %s, want the missed week 2026-W31", w.Label)
	}
}

func TestMondayAfterASuccessfulFridayReportsCurrentWeek(t *testing.T) {
	loc := denver(t)
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, loc)

	// Friday's run already went out.
	w := ResolveWindow(now, loc, "2026-W31")

	if w.CatchUp {
		t.Error("nothing was missed, so this is not a catch-up")
	}
	if w.Label != "2026-W32" {
		t.Errorf("label = %s, want the current week 2026-W32", w.Label)
	}
}

func TestSundayStillReportsTheWeekJustEnded(t *testing.T) {
	loc := denver(t)
	now := time.Date(2026, 8, 2, 20, 0, 0, 0, loc) // Sunday evening

	w := ResolveWindow(now, loc, "")

	if w.Label != "2026-W31" {
		t.Errorf("label = %s, want 2026-W31 (Sunday belongs to the week that just ended)", w.Label)
	}
}

func TestWindowNeverCoversTheFuture(t *testing.T) {
	loc := denver(t)
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, loc) // Wednesday

	w := ResolveWindow(now, loc, "2026-W30")

	if w.End.After(now) {
		t.Errorf("window end %v is in the future relative to %v", w.End, now)
	}
}

func TestNextWeekFollowsTheReportedWeek(t *testing.T) {
	loc := denver(t)
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, loc)

	w := ResolveWindow(now, loc, "")

	if got, want := w.NextStart.Format("2006-01-02"), "2026-08-03"; got != want {
		t.Errorf("next week starts %s, want %s", got, want)
	}
	if got, want := w.NextEnd.Format("2006-01-02"), "2026-08-10"; got != want {
		t.Errorf("next week ends %s, want %s", got, want)
	}
}

// Week boundaries must be computed in the mailbox's timezone. Durango is UTC-6
// in summer, so late-Sunday-evening local time is already Monday in UTC; using
// UTC would push events into the wrong week.
func TestWeekBoundaryUsesTheMailboxTimezone(t *testing.T) {
	loc := denver(t)
	sundayLate := time.Date(2026, 8, 2, 23, 30, 0, 0, loc)

	w := ResolveWindow(sundayLate, loc, "")

	if w.Label != "2026-W31" {
		t.Errorf("label = %s; late Sunday in Denver is still week 31 locally", w.Label)
	}
	if w.Start.Location() != loc {
		t.Errorf("window start is in %v, want %v", w.Start.Location(), loc)
	}
}
