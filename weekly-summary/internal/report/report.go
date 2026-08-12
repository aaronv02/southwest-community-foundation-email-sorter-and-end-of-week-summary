// Package report renders a Digest as an HTML email.
//
// Outlook's desktop client renders HTML with Microsoft Word's engine, which
// ignores flexbox, grid, most positioning, and external stylesheets. So this is
// deliberately old-fashioned: tables for layout, inline styles throughout, no
// classes, no <style> block relied upon. It looks plain because plain is what
// survives.
package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
	"time"

	"swcf/digest/internal/analyze"
	"swcf/digest/internal/graph"
)

//go:embed template.html
var templateSource string

// Render produces the HTML body of the digest email.
func Render(d analyze.Digest) (string, error) {
	loc := d.Location
	if loc == nil {
		loc = time.UTC
	}

	funcs := template.FuncMap{
		"day":  func(t time.Time) string { return t.In(loc).Format("Mon 2 Jan") },
		"time": func(t time.Time) string { return t.In(loc).Format("3:04 PM") },
		"dayTime": func(t time.Time) string {
			return t.In(loc).Format("Mon 2 Jan, 3:04 PM")
		},
		"eventStart": func(e graph.Event) string {
			t := e.Start.Time(loc)
			if t.IsZero() {
				return ""
			}
			if e.IsAllDay {
				return t.Format("Mon 2 Jan") + ", all day"
			}
			return t.Format("Mon 2 Jan, 3:04 PM")
		},
		"eventLength": func(e graph.Event) string {
			d := e.Duration(loc)
			if d <= 0 {
				return ""
			}
			return humanDuration(d)
		},
		"age":      humanAge,
		"plural":   plural,
		"hours":    func(h float64) string { return trimFloat(h) },
		"truncate": truncate,
		"subject": func(s string) string {
			s = strings.TrimSpace(s)
			if s == "" {
				return "(no subject)"
			}
			return truncate(s, 90)
		},
		"joinSubjects": func(subjects []string) string {
			cleaned := make([]string, 0, len(subjects))
			for _, s := range subjects {
				s = strings.TrimSpace(s)
				if s == "" {
					s = "(no subject)"
				}
				cleaned = append(cleaned, truncate(s, 60))
			}
			return strings.Join(cleaned, " · ")
		},
		"limit": func(n int, items []analyze.SenderGroup) []analyze.SenderGroup {
			if len(items) > n {
				return items[:n]
			}
			return items
		},
		"limitWaiting": func(n int, items []analyze.WaitingItem) []analyze.WaitingItem {
			if len(items) > n {
				return items[:n]
			}
			return items
		},
		"limitMessages": func(n int, items []graph.Message) []graph.Message {
			if len(items) > n {
				return items[:n]
			}
			return items
		},
		"limitEvents": func(n int, items []graph.Event) []graph.Event {
			if len(items) > n {
				return items[:n]
			}
			return items
		},
		"sub": func(a, b int) int { return a - b },
	}

	tmpl, err := template.New("digest").Funcs(funcs).Parse(templateSource)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("rendering template: %w", err)
	}
	return buf.String(), nil
}

// Subject builds the email subject line.
func Subject(d analyze.Digest) string {
	prefix := "Your week"
	if d.Window.CatchUp {
		prefix = "Last week"
	}
	return fmt.Sprintf("%s: %s (%s)", prefix, d.Headline(), d.Window.Describe())
}

func humanAge(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days >= 14:
		return fmt.Sprintf("%d weeks", days/7)
	case days >= 7:
		return "over a week"
	case days >= 1:
		return fmt.Sprintf("%d %s", days, plural(days, "day", "days"))
	default:
		h := int(d.Hours())
		if h < 1 {
			return "under an hour"
		}
		return fmt.Sprintf("%d %s", h, plural(h, "hour", "hours"))
	}
}

func humanDuration(d time.Duration) string {
	mins := int(d.Minutes())
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	h := float64(mins) / 60
	return trimFloat(h) + " hr"
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
