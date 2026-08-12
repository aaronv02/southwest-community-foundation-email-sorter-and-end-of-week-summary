package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
)

// Interactive sign-in, for mailboxes where app-only authentication is not an
// option - most importantly personal outlook.com accounts, which Microsoft
// excludes from the client-credentials flow entirely.

const loginAuthority = "https://login.microsoftonline.com"

// runLogin signs in as a user and saves the resulting session.
func runLogin() error {
	in := bufio.NewReader(os.Stdin)

	cfg, err := config.Load()
	if err != nil && !errors.Is(err, config.ErrNotConfigured) {
		// A config that fails validation only because it is not signed in yet
		// is exactly the case this command exists to fix, so carry on with it.
		cfg = nil
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	fmt.Println("Sign in to Outlook")
	fmt.Println("------------------")
	fmt.Println("This signs in as YOU, rather than as a background application.")
	fmt.Println("It works with any Outlook account, including personal")
	fmt.Println("outlook.com addresses, and needs no administrator approval.")
	fmt.Println()

	if cfg.ClientID == "" {
		fmt.Println("You need an application (client) ID from an Entra app registration.")
		fmt.Println("See README, \"Testing against your own mailbox\".")
		fmt.Println()
	}
	cfg.ClientID = ask(in, "Application (client) ID", cfg.ClientID)
	if strings.TrimSpace(cfg.ClientID) == "" {
		return fmt.Errorf("a client ID is required")
	}

	// "common" accepts both work/school and personal accounts. A tenant GUID
	// here would lock out exactly the personal mailboxes this mode exists for.
	tenant := cfg.TenantID
	if tenant == "" {
		tenant = "common"
	}
	cfg.TenantID = ask(in, "Tenant (use 'common' unless told otherwise)", tenant)

	cfg.Mailbox = ask(in, "Your email address", cfg.Mailbox)
	if strings.TrimSpace(cfg.Mailbox) == "" {
		return fmt.Errorf("an email address is required")
	}
	if len(cfg.Recipients) == 0 {
		cfg.Recipients = []string{cfg.Mailbox}
	}
	cfg.AuthMode = "delegated"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	prompt, err := graph.StartDeviceCode(ctx, loginAuthority, cfg.TenantID, cfg.ClientID)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────┐")
	fmt.Printf("  │  1. Open:  %-32s │\n", prompt.VerificationURL)
	fmt.Printf("  │  2. Enter code:  %-26s │\n", prompt.UserCode)
	fmt.Println("  │  3. Sign in and approve the permissions      │")
	fmt.Println("  └─────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Waiting for you to finish in the browser…")

	refreshToken, err := graph.PollDeviceCode(ctx, loginAuthority, cfg.TenantID, cfg.ClientID, prompt)
	if err != nil {
		return err
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	if err := cfg.SetRefreshToken(refreshToken); err != nil {
		return fmt.Errorf("saving the sign-in: %w", err)
	}

	fmt.Println()
	fmt.Print("Signed in. Checking mailbox access… ")

	client := graph.New(cfg.TenantID, cfg.ClientID, "", cfg.Mailbox, cfg.Timezone)
	client.UseDelegated(refreshToken, func(rotated string) {
		_ = cfg.SetRefreshToken(rotated)
	})

	if err := client.Ping(ctx); err != nil {
		fmt.Println("failed.")
		return err
	}
	fmt.Println("confirmed.")

	dir, _ := config.Dir()
	fmt.Println()
	fmt.Printf("Saved to %s\n", dir)
	fmt.Printf("Sign-in is %s.\n", config.SecretProtectionNote)
	fmt.Println()
	fmt.Println("Now try:")
	fmt.Println("  digest --preview week.html     see the summary, send nothing")
	fmt.Println("  digest --dry-run               everything except sending")
	fmt.Println()
	fmt.Println("Note: this saved sign-in expires after a long gap without use, or if")
	fmt.Println("you change your password. When it does, run --login again. That is")
	fmt.Println("why the scheduled Friday job uses application auth instead.")
	return nil
}

// newClient builds a Graph client honouring the configured authentication mode.
func newClient(cfg *config.Config) (*graph.Client, error) {
	if cfg.Delegated() {
		refresh, err := cfg.RefreshToken()
		if err != nil {
			return nil, fmt.Errorf("reading the saved sign-in: %w\nRun:  digest --login", err)
		}
		client := graph.New(cfg.TenantID, cfg.ClientID, "", cfg.Mailbox, cfg.Timezone)
		client.UseDelegated(refresh, func(rotated string) {
			if err := cfg.SetRefreshToken(rotated); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not save the rotated sign-in: %v\n", err)
			}
		})
		return client, nil
	}

	secret, err := cfg.Secret()
	if err != nil {
		return nil, err
	}
	return graph.New(cfg.TenantID, cfg.ClientID, secret, cfg.Mailbox, cfg.Timezone), nil
}
