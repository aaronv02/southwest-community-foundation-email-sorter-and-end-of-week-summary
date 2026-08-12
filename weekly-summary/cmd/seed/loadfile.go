package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"swcf/digest/internal/graph"
)

// Loading generated test data from a JSON file.
//
// The point of the `expect` field is that generated data is only useful if you
// know what it is supposed to prove. Each message carries its own expectation,
// and after injecting, the tool prints a checklist grouped by expectation - so
// the preview can be checked against a spec rather than eyeballed for vibes.

// FileMessage is one message in a test-data file.
type FileMessage struct {
	Subject     string  `json:"subject"`
	Body        string  `json:"body"`
	FromName    string  `json:"fromName"`
	FromAddress string  `json:"fromAddress"`
	DaysAgo     float64 `json:"daysAgo"`
	IsRead      bool    `json:"isRead"`
	Flagged     bool    `json:"flagged"`
	// Place in Sent Items rather than the inbox.
	Sent bool `json:"sent"`
	// She is only CC'd, not on the To line.
	CCOnly bool `json:"ccOnly"`

	// What this message is meant to demonstrate. One of:
	//   waiting        - should appear under "Still waiting on you"
	//   not-waiting    - should NOT appear there, and `why` says why
	//   unread-person  - should appear under unread, from a person
	//   unread-bulk    - should appear under unread, grouped as automated
	//   sent           - contributes to the week-in-review counts
	Expect string `json:"expect"`
	// One line explaining the expectation, shown in the checklist.
	Why string `json:"why"`
}

// FileEvent is one calendar entry in a test-data file.
type FileEvent struct {
	Subject   string  `json:"subject"`
	DaysAgo   float64 `json:"daysAgo"`
	StartHour int     `json:"startHour"`
	Hours     float64 `json:"hours"`
	Attendees int     `json:"attendees"`
	AllDay    bool    `json:"allDay"`
	Why       string  `json:"why"`
}

// SeedFile is the whole document.
type SeedFile struct {
	Messages []FileMessage `json:"messages"`
	Events   []FileEvent   `json:"events"`
}

func loadSeedFile(path string) (*SeedFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f SeedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(f.Messages) == 0 && len(f.Events) == 0 {
		return nil, fmt.Errorf("%s contains no messages or events", path)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

var validExpectations = map[string]bool{
	"waiting": true, "not-waiting": true,
	"unread-person": true, "unread-bulk": true, "sent": true,
	"": true, // tolerated: the message just adds volume
}

// validate catches generated data that would silently test nothing - a message
// with no sender, or an expectation nobody will check.
func (f *SeedFile) validate() error {
	var problems []string
	for i, m := range f.Messages {
		switch {
		case strings.TrimSpace(m.Subject) == "":
			problems = append(problems, fmt.Sprintf("message %d has no subject", i))
		case strings.TrimSpace(m.FromAddress) == "":
			problems = append(problems, fmt.Sprintf("message %d (%q) has no fromAddress", i, m.Subject))
		case !strings.Contains(m.FromAddress, "@"):
			problems = append(problems, fmt.Sprintf("message %d has a malformed address %q", i, m.FromAddress))
		case !validExpectations[m.Expect]:
			problems = append(problems, fmt.Sprintf("message %d has unknown expect %q", i, m.Expect))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("test data has problems:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func injectFile(ctx context.Context, c *graph.Client, f *SeedFile, now time.Time, tz string) error {
	fmt.Printf("Injecting %d message(s) and %d event(s) from file…\n",
		len(f.Messages), len(f.Events))

	for _, m := range f.Messages {
		msg := graph.SeedMessage{
			Subject:     m.Subject,
			Body:        m.Body,
			FromName:    m.FromName,
			FromAddress: m.FromAddress,
			Received:    now.Add(-days(m.DaysAgo)),
			IsRead:      m.IsRead,
			Flagged:     m.Flagged,
			Sent:        m.Sent,
			CcMailbox:   m.CCOnly,
		}
		if m.CCOnly {
			// Addressed to someone else entirely, with her merely copied.
			msg.ToOverride = "someone.else@example.org"
		}
		if err := c.CreateMessage(ctx, msg); err != nil {
			return fmt.Errorf("creating %q: %w", m.Subject, err)
		}
	}

	monday := startOfWeek(now)
	for _, e := range f.Events {
		start := monday.Add(days(e.DaysAgo) + time.Duration(e.StartHour)*time.Hour)
		hours := e.Hours
		if hours <= 0 {
			hours = 1
		}
		ev := graph.SeedEvent{
			Subject:   e.Subject,
			Start:     start,
			End:       start.Add(time.Duration(hours * float64(time.Hour))),
			Attendees: e.Attendees,
			AllDay:    e.AllDay,
		}
		if err := c.CreateEvent(ctx, ev, tz); err != nil {
			return fmt.Errorf("creating event %q: %w", e.Subject, err)
		}
	}

	printChecklist(f)
	return nil
}

// printChecklist turns the file's expectations into something to check the
// preview against.
func printChecklist(f *SeedFile) {
	groups := map[string][]FileMessage{}
	for _, m := range f.Messages {
		key := m.Expect
		if key == "" {
			key = "(no stated expectation)"
		}
		groups[key] = append(groups[key], m)
	}

	labels := map[string]string{
		"waiting":                 "MUST appear under 'Still waiting on you'",
		"not-waiting":             "MUST NOT appear under 'Still waiting on you'",
		"unread-person":           "MUST appear under unread, from a person",
		"unread-bulk":             "MUST appear under unread, grouped as automated",
		"sent":                    "counts toward the week-in-review totals",
		"(no stated expectation)": "no expectation given - adds volume only",
	}

	order := []string{"waiting", "not-waiting", "unread-person", "unread-bulk",
		"sent", "(no stated expectation)"}

	fmt.Println("\nNow run:  digest --preview week.html")
	fmt.Println("\nCheck the preview against this:")

	for _, key := range order {
		items := groups[key]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("\n  %s (%d)\n", labels[key], len(items))

		// Oldest first, matching how the digest orders the waiting list.
		sort.SliceStable(items, func(i, j int) bool { return items[i].DaysAgo > items[j].DaysAgo })
		for _, m := range items {
			line := fmt.Sprintf("    [ ] %s", truncateStr(m.Subject, 58))
			if m.Why != "" {
				line += fmt.Sprintf("\n          %s", m.Why)
			}
			fmt.Println(line)
		}
	}

	if len(f.Events) > 0 {
		fmt.Printf("\n  Calendar entries created (%d)\n", len(f.Events))
		for _, e := range f.Events {
			fmt.Printf("    [ ] %s\n", truncateStr(e.Subject, 58))
		}
	}

	fmt.Println("\nReminder: injected mail cannot thread, so reply detection is NOT")
	fmt.Println("covered here. Use --threads for that. See TESTING.md.")
}

func truncateStr(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
