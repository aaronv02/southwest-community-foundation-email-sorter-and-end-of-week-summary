package config

import (
	"encoding/base64"
	"fmt"
	"syscall"
	"unsafe"
)

// Windows secret protection via DPAPI.
//
// CryptProtectData encrypts against the current Windows user account, so the
// ciphertext is only recoverable by that same user on that same machine. If the
// laptop is stolen and the disk read, config.json yields nothing usable. This
// matters here: the client secret it protects can read a mailbox holding donor
// and grant correspondence.
//
// Implemented against crypt32.dll directly rather than pulling in
// golang.org/x/sys, which keeps the module dependency-free and the binary small.

var (
	crypt32              = syscall.NewLazyDLL("crypt32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect   = crypt32.NewProc("CryptUnprotectData")
	procLocalFree        = kernel32.NewProc("LocalFree")
)

// cryptProtectUIForbidden. Required for unattended use: without it DPAPI may
// try to show a UI prompt, which under a scheduled task means it simply hangs.
const cryptProtectUIForbidden = 0x1

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func (b *dataBlob) free() {
	if b.pbData != nil {
		procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
		b.pbData = nil
	}
}

func protect(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	in := newBlob([]byte(plaintext))
	var out dataBlob

	ret, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // description
		0, // no additional entropy
		0, // reserved
		0, // no prompt struct
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return "", fmt.Errorf("DPAPI encryption failed: %w", err)
	}
	defer out.free()

	return base64.StdEncoding.EncodeToString(out.bytes()), nil
}

func unprotect(encoded string) (string, error) {
	if encoded == "" {
		return "", fmt.Errorf("no stored secret")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("stored secret is not valid base64: %w", err)
	}

	in := newBlob(raw)
	var out dataBlob

	ret, _, callErr := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // description out
		0, // no additional entropy
		0, // reserved
		0, // no prompt struct
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return "", fmt.Errorf(
			"could not decrypt the stored secret: %w\n"+
				"This usually means the config was created by a different Windows user "+
				"or copied from another machine. Re-run with --setup to store it again.",
			callErr)
	}
	defer out.free()

	return string(out.bytes()), nil
}

// SecretProtectionNote describes how the secret is stored, for the setup output.
const SecretProtectionNote = "encrypted with Windows DPAPI for the current user account"
