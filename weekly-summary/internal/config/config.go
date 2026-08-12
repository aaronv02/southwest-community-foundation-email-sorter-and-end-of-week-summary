// Package config loads and stores the digest's settings.
//
// The client secret is the sensitive part. On Windows it is encrypted with
// DPAPI, tied to the Windows user account that installed it, so a copied
// config file is useless on any other machine or under any other login. On
// macOS and Linux (development only) it falls back to a 0600 file, which is
// weaker and says so out loud.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config is everything the digest needs to run unattended.
type Config struct {
	// Entra directory (tenant) ID.
	TenantID string `json:"tenantId"`
	// Entra application (client) ID.
	ClientID string `json:"clientId"`
	// DPAPI-protected client secret, base64. Never the plaintext.
	ProtectedSecret string `json:"protectedSecret"`

	// The mailbox to analyze, e.g. "director@example.org".
	Mailbox string `json:"mailbox"`

	// Other addresses that reach this person: role aliases like
	// info@ or grants@, and any shared mailbox she is a member of.
	//
	// This exists because "was she actually asked?" is decided by looking for
	// her address on the To line. Mail sent to an alias the digest doesn't know
	// about looks like mail addressed to a stranger, so it never appears as
	// waiting - and the failure is silent, showing up as a suspiciously short
	// list rather than an error.
	AlsoAddressedAs []string `json:"alsoAddressedAs"`
	// Where the digest is emailed. Usually the same as Mailbox.
	Recipients []string `json:"recipients"`

	// IANA timezone used for week boundaries and display. The foundation is in
	// Durango, Colorado, so this defaults to America/Denver. Getting this wrong
	// shifts the whole reporting window, which is why it is explicit rather
	// than inferred from the machine.
	Timezone string `json:"timezone"`

	// Hours an email may sit unanswered before it counts as waiting. A grace
	// period matters: mail that arrived this morning is not a failure.
	WaitingGraceHours int `json:"waitingGraceHours"`

	// Senders never counted as "waiting on you" - newsletters, no-reply
	// addresses, and anything else that does not expect a human reply.
	// Matched as substrings against the sender address, case-insensitive.
	IgnoredSenderPatterns []string `json:"ignoredSenderPatterns"`
}

// State is the small amount of memory the digest keeps between runs.
type State struct {
	// ISO year and week of the last digest actually sent. Guards against
	// double-sending when a missed run catches up.
	LastSentWeek string `json:"lastSentWeek"`
	LastRunAt    string `json:"lastRunAt"`
	LastError    string `json:"lastError,omitempty"`
}

// DefaultIgnoredSenders covers the usual automated traffic. These are only
// excluded from the "waiting on you" section - they still appear under unread,
// because "I never opened it" is true regardless of who sent it.
// Patterns ending in "@" are anchored to the start of the local part; patterns
// starting with "@" match the domain; anything else is a substring match.
// Notably absent: "info@" and "events@", which at a small nonprofit are often
// staffed by an actual person who does expect a reply.
var DefaultIgnoredSenders = []string{
	"no-reply@", "noreply@", "donotreply@", "do-not-reply@", "no_reply@",
	"notifications@", "notification@", "mailer-daemon@", "postmaster@",
	"automated@", "auto@", "alerts@", "alert@",
	"newsletter@", "news@", "updates@", "update@", "bulletin@", "digest@",
	"announcements@", "announce@", "marketing@", "campaigns@",
	"bounce", "unsubscribe",
	"@mailchimp", "@sendgrid", "@constantcontact", "@salsalabs",
	"@mailgun", "@sparkpostmail", "@amazonses",
}

// Dir returns the per-user configuration directory.
//
// %APPDATA%\SWCFDigest on Windows; ~/.config/swcf-digest elsewhere. Per-user
// rather than machine-wide because DPAPI protection is per-user, and because a
// scheduled task runs as a specific account.
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "SWCFDigest"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", "swcf-digest"), nil
}

func configPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func statePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// ErrNotConfigured is returned when no config file exists yet.
var ErrNotConfigured = errors.New("not configured yet - run with --setup")

// Load reads the configuration and applies defaults.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg.applyDefaults()
	return &cfg, cfg.Validate()
}

func (c *Config) applyDefaults() {
	if c.Timezone == "" {
		c.Timezone = "America/Denver"
	}
	if c.WaitingGraceHours == 0 {
		c.WaitingGraceHours = 48
	}
	if len(c.IgnoredSenderPatterns) == 0 {
		c.IgnoredSenderPatterns = DefaultIgnoredSenders
	}
	if len(c.Recipients) == 0 && c.Mailbox != "" {
		c.Recipients = []string{c.Mailbox}
	}
}

// Validate catches misconfiguration before any network call, so failures are
// legible rather than surfacing as an opaque 401 an hour later.
func (c *Config) Validate() error {
	var missing []string
	if c.TenantID == "" {
		missing = append(missing, "tenantId")
	}
	if c.ClientID == "" {
		missing = append(missing, "clientId")
	}
	if c.ProtectedSecret == "" {
		missing = append(missing, "client secret")
	}
	if c.Mailbox == "" {
		missing = append(missing, "mailbox")
	}
	if len(missing) > 0 {
		return fmt.Errorf("configuration incomplete: missing %s", strings.Join(missing, ", "))
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q: %w", c.Timezone, err)
	}
	return nil
}

// Addresses returns every address that counts as "her", primary first.
func (c *Config) Addresses() []string {
	out := []string{c.Mailbox}
	seen := map[string]bool{strings.ToLower(c.Mailbox): true}
	for _, a := range c.AlsoAddressedAs {
		a = strings.TrimSpace(a)
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	return out
}

// Location resolves the configured timezone.
func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Save writes the configuration with restrictive permissions.
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Secret decrypts the stored client secret.
func (c *Config) Secret() (string, error) {
	return unprotect(c.ProtectedSecret)
}

// SetSecret encrypts and stores the client secret.
func (c *Config) SetSecret(plaintext string) error {
	protected, err := protect(plaintext)
	if err != nil {
		return err
	}
	c.ProtectedSecret = protected
	return nil
}

// LoadState reads run memory, treating absence as a clean slate.
func LoadState() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		// Corrupt state should never block a digest; the worst case is one
		// duplicate email, which beats silently sending nothing.
		return &State{}, nil
	}
	return &s, nil
}

// Save persists run memory.
func (s *State) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path, err := statePath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
