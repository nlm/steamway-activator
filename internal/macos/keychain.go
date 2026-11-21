// Package macos wraps the macOS security and osascript commands behind an
// injectable Runner so callers can be tested on any OS.
package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec is the default Runner: it runs the command and returns its stdout.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ReadSecret reads a generic password from the macOS Keychain.
func ReadSecret(ctx context.Context, run Runner, service, account string) (string, error) {
	out, err := run(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return "", fmt.Errorf("read keychain item %q: %w", service, err)
	}
	secret := strings.TrimRight(string(out), "\r\n")
	if secret == "" {
		return "", errors.New("keychain item " + service + " is empty")
	}
	return secret, nil
}
