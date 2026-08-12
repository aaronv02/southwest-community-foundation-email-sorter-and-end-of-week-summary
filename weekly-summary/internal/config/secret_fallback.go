//go:build !windows

package config

import (
	"encoding/base64"
	"fmt"
)

// Non-Windows secret handling: development only.
//
// The digest ships to a Windows machine, where DPAPI protects the secret
// against the logged-in user. There is no equivalent here that would be honest
// to call encryption, so this deliberately does not pretend: the value is
// base64-encoded (obfuscation, not protection) and the config file is written
// 0600. Setup prints a warning saying exactly that.
//
// This path exists so the whole pipeline can be developed and tested on macOS,
// which matters because the build machine is not the target machine.

func protect(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func unprotect(encoded string) (string, error) {
	if encoded == "" {
		return "", fmt.Errorf("no stored secret")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("stored secret is not valid base64: %w", err)
	}
	return string(raw), nil
}

// SecretProtectionNote describes how the secret is stored, for the setup output.
const SecretProtectionNote = "base64-encoded in a 0600 file (NOT encrypted - development platform only)"
