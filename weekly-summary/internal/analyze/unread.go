package analyze

import (
	"sort"
	"strings"
	"time"

	"swcf/digest/internal/graph"
)

// SenderGroup collects unread mail from one sender.
type SenderGroup struct {
	Address  string
	Name     string
	Count    int
	Oldest   time.Time
	Newest   time.Time
	Subjects []string
	// True when the sender looks like a list or robot.
	Automated bool
}

// UnreadReport separates unread mail that a person sent from bulk traffic.
type UnreadReport struct {
	People    []SenderGroup
	Automated []SenderGroup
	Total     int
	// Unread messages older than a week, which is the number worth worrying about.
	StaleCount int
}

// Unread groups everything she never opened.
//
// Grouped by sender rather than listed chronologically, and split into people
// versus bulk. "37 unread" is a number that produces guilt and no action;
// "Tessa Nunn sent 3 you haven't opened, plus 22 newsletters" is something you
// can actually do something about in thirty seconds.
func Unread(
	inbox []graph.Message,
	addresses []string,
	now time.Time,
	ignoredPatterns []string,
) UnreadReport {
	own := make(map[string]bool, len(addresses))
	for _, a := range addresses {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			own[a] = true
		}
	}
	groups := make(map[string]*SenderGroup)
	report := UnreadReport{}

	for _, m := range inbox {
		if m.IsRead || m.IsDraft {
			continue
		}
		from := m.FromAddress()
		if from == "" || own[from] {
			continue
		}

		report.Total++
		if now.Sub(m.ReceivedDateTime) > 7*24*time.Hour {
			report.StaleCount++
		}

		g, ok := groups[from]
		if !ok {
			g = &SenderGroup{
				Address:   from,
				Name:      m.FromName(),
				Oldest:    m.ReceivedDateTime,
				Newest:    m.ReceivedDateTime,
				Automated: isAutomatedSender(from, ignoredPatterns),
			}
			groups[from] = g
		}
		g.Count++
		if m.ReceivedDateTime.Before(g.Oldest) {
			g.Oldest = m.ReceivedDateTime
		}
		if m.ReceivedDateTime.After(g.Newest) {
			g.Newest = m.ReceivedDateTime
		}
		// Keep a few subjects for context without turning the digest into a
		// second inbox.
		if len(g.Subjects) < 3 {
			g.Subjects = append(g.Subjects, m.Subject)
		}
	}

	for _, g := range groups {
		if g.Automated {
			report.Automated = append(report.Automated, *g)
		} else {
			report.People = append(report.People, *g)
		}
	}

	// People first by volume, then by age; bulk purely by volume since it is
	// really just a cleanup list.
	sort.SliceStable(report.People, func(i, j int) bool {
		if report.People[i].Count != report.People[j].Count {
			return report.People[i].Count > report.People[j].Count
		}
		return report.People[i].Oldest.Before(report.People[j].Oldest)
	})
	sort.SliceStable(report.Automated, func(i, j int) bool {
		return report.Automated[i].Count > report.Automated[j].Count
	})

	return report
}
