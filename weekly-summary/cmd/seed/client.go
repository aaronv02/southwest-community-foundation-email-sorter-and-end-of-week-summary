package main

import (
	"fmt"
	"os"

	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
)

// newClient builds a Graph client honouring the configured authentication mode.
//
// Duplicated from cmd/digest rather than shared: these are separate commands,
// and a shared internal package for eight lines would couple them for no gain.
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
