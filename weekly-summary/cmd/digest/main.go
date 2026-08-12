// Command digest builds and emails a weekly Outlook summary.
//
// Run with no arguments it does what the scheduled task needs: work out which
// week to report, fetch, render, send, and record that it sent. Every other
// mode exists for setup and for debugging from a machine that is not the one
// it runs on.
//
//	digest --setup      configure credentials and mailbox
//	digest --check      verify credentials and mailbox access, change nothing
//	digest --preview    write the email to an HTML file instead of sending
//	digest --dry-run    do everything except send
//	digest --force      send even if this week was already sent
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swcf/digest/internal/analyze"
	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
	"swcf/digest/internal/report"
)

// version is stamped at build time via -ldflags.
var version = "dev"

const (
	// How far back to look for mail that is still unanswered. Reaching beyond
	// the reporting week is the point: something that arrived three weeks ago
	// and was never answered is exactly what should surface.
	waitingLookback = 21 * 24 * time.Hour
	// Sent mail is fetched further back still, so an old reply correctly
	// silences an old inbound message.
	sentLookback = 30 * 24 * time.Hour
)

func main() {
	var (
		setup   = flag.Bool("setup", false, "configure credentials and mailbox")
		check   = flag.Bool("check", false, "verify access and exit")
		preview = flag.String("preview", "", "write the email to this HTML file instead of sending")
		dryRun  = flag.Bool("dry-run", false, "do everything except send")
		force   = flag.Bool("force", false, "send even if this week was already sent")
		showVer = flag.Bool("version", false, "print version and exit")
		mcp     = flag.Bool("mcp", false, "run as an MCP server for Claude Desktop")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("swcf weekly digest", version)
		return
	}

	// MCP mode owns stdout for the protocol, so it must be handled before any
	// other code path has a chance to print to it.
	if *mcp {
		if err := runMCP(version); err != nil {
			fmt.Fprintf(os.Stderr, "\n%v\n", err)
			os.Exit(1)
		}
		return
	}

	if *setup {
		if err := runSetup(); err != nil {
			fatal(err)
		}
		return
	}

	logFile, err := openLog()
	if err != nil {
		// Logging is a convenience, not a precondition.
		fmt.Fprintf(os.Stderr, "warning: could not open log file: %v\n", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	if err := run(*check, *preview, *dryRun, *force, logFile); err != nil {
		logf(logFile, "ERROR %v", err)
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "\n%v\n", err)
	os.Exit(1)
}

// openLog appends to a rolling log beside the config.
//
// The digest runs unattended on someone else's laptop. Without a log on disk,
// a failure three weeks from now is undiagnosable without physically sitting at
// that machine.
func openLog() (*os.File, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "digest.log")

	// Keep the log from growing without bound on a machine nobody prunes.
	if info, err := os.Stat(path); err == nil && info.Size() > 512*1024 {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func logf(w io.Writer, format string, args ...any) {
	line := fmt.Sprintf("%s  %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	fmt.Print(line)
	if w != nil {
		fmt.Fprint(w, line)
	}
}

func run(check bool, previewPath string, dryRun, force bool, logw io.Writer) error {
	cfg, err := config.Load()
	if errors.Is(err, config.ErrNotConfigured) {
		return fmt.Errorf("no configuration found.\nRun:  digest --setup")
	}
	if err != nil {
		return err
	}

	secret, err := cfg.Secret()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := graph.New(cfg.TenantID, cfg.ClientID, secret, cfg.Mailbox, cfg.Timezone)

	if check {
		if err := client.Ping(ctx); err != nil {
			return err
		}
		fmt.Printf("Access confirmed for %s.\n", cfg.Mailbox)
		fmt.Printf("Timezone %s. Digest goes to: %s\n",
			cfg.Timezone, strings.Join(cfg.Recipients, ", "))
		return nil
	}

	state, err := config.LoadState()
	if err != nil {
		return err
	}

	loc := cfg.Location()
	now := time.Now().In(loc)
	window := analyze.ResolveWindow(now, loc, state.LastSentWeek)

	logf(logw, "run: week=%s catchUp=%v window=%s..%s",
		window.Label, window.CatchUp,
		window.Start.Format(time.RFC3339), window.End.Format(time.RFC3339))

	sending := previewPath == "" && !dryRun
	if sending && !force && state.LastSentWeek == window.Label {
		logf(logw, "already sent %s; nothing to do", window.Label)
		fmt.Println("This week's digest was already sent. Use --force to send it again.")
		return nil
	}

	// --- fetch -------------------------------------------------------------
	logf(logw, "fetching mailbox data")

	inbox, err := client.InboxSince(ctx, window.Start.Add(-waitingLookback))
	if err != nil {
		return fmt.Errorf("reading inbox: %w", err)
	}
	sent, err := client.SentSince(ctx, window.Start.Add(-sentLookback))
	if err != nil {
		return fmt.Errorf("reading sent items: %w", err)
	}
	events, err := client.CalendarView(ctx, window.Start, window.NextEnd)
	if err != nil {
		return fmt.Errorf("reading calendar: %w", err)
	}

	logf(logw, "fetched %d inbox, %d sent, %d events", len(inbox), len(sent), len(events))

	// --- analyze -----------------------------------------------------------
	digest := analyze.Build(
		analyze.Input{Inbox: inbox, Sent: sent, Events: events},
		window,
		analyze.Options{
			Mailbox:         cfg.Mailbox,
			Addresses:       cfg.Addresses(),
			Location:        loc,
			Now:             now,
			WaitingGrace:    time.Duration(cfg.WaitingGraceHours) * time.Hour,
			IgnoredPatterns: cfg.IgnoredSenderPatterns,
		},
	)

	logf(logw, "analysis: waiting=%d unread=%d flagged=%d meetings=%d sent=%d",
		len(digest.Waiting), digest.Unread.Total, len(digest.Flagged),
		digest.Review.MeetingsHeld, digest.Review.EmailsSent)

	html, err := report.Render(digest)
	if err != nil {
		return err
	}
	subject := report.Subject(digest)

	// --- deliver -----------------------------------------------------------
	if previewPath != "" {
		if err := os.WriteFile(previewPath, []byte(html), 0o600); err != nil {
			return fmt.Errorf("writing preview: %w", err)
		}
		abs, _ := filepath.Abs(previewPath)
		fmt.Printf("Subject: %s\n", subject)
		fmt.Printf("Preview written to %s\n", abs)
		return nil
	}

	if dryRun {
		fmt.Printf("Subject: %s\n", subject)
		fmt.Printf("Would send to: %s\n", strings.Join(cfg.Recipients, ", "))
		fmt.Println("Dry run - nothing sent.")
		return nil
	}

	if err := client.SendMail(ctx, subject, html, cfg.Recipients); err != nil {
		state.LastError = err.Error()
		state.LastRunAt = now.Format(time.RFC3339)
		_ = state.Save()
		return fmt.Errorf("sending digest: %w", err)
	}

	state.LastSentWeek = window.Label
	state.LastRunAt = now.Format(time.RFC3339)
	state.LastError = ""
	if err := state.Save(); err != nil {
		// The mail is already gone; a state-write failure would only cause a
		// duplicate next run, which is not worth failing the whole run over.
		logf(logw, "warning: could not save state: %v", err)
	}

	logf(logw, "sent %s to %s", window.Label, strings.Join(cfg.Recipients, ", "))
	fmt.Printf("Sent: %s\n", subject)
	return nil
}

// runSetup walks through configuration interactively.
func runSetup() error {
	in := bufio.NewReader(os.Stdin)

	fmt.Println("Weekly digest setup")
	fmt.Println("-------------------")
	fmt.Println("You'll need the values from the Entra app registration (see README).")
	fmt.Println()

	existing, err := config.Load()
	if err != nil && !errors.Is(err, config.ErrNotConfigured) {
		// A partially valid config should not block reconfiguring.
		existing = nil
	}

	cfg := &config.Config{}
	if existing != nil {
		cfg = existing
		fmt.Println("Existing configuration found; press Enter to keep current values.")
		fmt.Println()
	}

	cfg.TenantID = ask(in, "Directory (tenant) ID", cfg.TenantID)
	cfg.ClientID = ask(in, "Application (client) ID", cfg.ClientID)

	secretPrompt := "Client secret VALUE"
	if cfg.ProtectedSecret != "" {
		secretPrompt += " (Enter to keep existing)"
	}
	if secret := ask(in, secretPrompt, ""); secret != "" {
		if err := cfg.SetSecret(secret); err != nil {
			return fmt.Errorf("storing secret: %w", err)
		}
	}

	cfg.Mailbox = ask(in, "Mailbox to summarize", cfg.Mailbox)

	// Asked explicitly because getting it wrong fails silently: mail sent to an
	// unknown alias never registers as addressed to her, so the waiting list
	// just comes back short with nothing to explain it.
	fmt.Println()
	fmt.Println("Does she also receive mail at other addresses? Role aliases like")
	fmt.Println("info@ or grants@, or a shared mailbox. Mail sent to an address")
	fmt.Println("listed here still counts as being addressed to her.")
	aliases := ask(in, "Other addresses (comma separated, blank for none)",
		strings.Join(cfg.AlsoAddressedAs, ","))
	cfg.AlsoAddressedAs = splitAndTrim(aliases)

	defaultRecipients := cfg.Mailbox
	if len(cfg.Recipients) > 0 {
		defaultRecipients = strings.Join(cfg.Recipients, ",")
	}
	recipients := ask(in, "Send the digest to (comma separated)", defaultRecipients)
	cfg.Recipients = splitAndTrim(recipients)

	tz := cfg.Timezone
	if tz == "" {
		tz = "America/Denver"
	}
	cfg.Timezone = ask(in, "Timezone", tz)

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	dir, _ := config.Dir()
	fmt.Println()
	fmt.Printf("Saved to %s\n", filepath.Join(dir, "config.json"))
	fmt.Printf("Secret is %s.\n", config.SecretProtectionNote)

	fmt.Println()
	fmt.Print("Verifying access… ")
	secret, err := cfg.Secret()
	if err != nil {
		return err
	}
	client := graph.New(cfg.TenantID, cfg.ClientID, secret, cfg.Mailbox, cfg.Timezone)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		fmt.Println("failed.")
		return err
	}
	fmt.Println("confirmed.")
	fmt.Println()
	fmt.Println("Next: run  digest --preview week.html  to see what the email will look like.")
	return nil
}

func ask(in *bufio.Reader, prompt, current string) string {
	if current != "" {
		fmt.Printf("%s [%s]: ", prompt, current)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return current
	}
	return line
}

func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
