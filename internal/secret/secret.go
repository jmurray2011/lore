// Package secret resolves a secret by running a command and reading its stdout,
// so a passphrase or key never lands in a flag value, in argv, or on disk. It is
// the single "run a command, read a secret" path — shared by the export
// passphrase here and, when it lands, the provider API key (LORE_API_KEY_CMD).
package secret

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// FromCommand runs command through the platform shell and returns its stdout,
// stripped of a trailing newline, as the secret. A non-zero exit or empty output
// is an error. Running via the shell lets callers write real invocations like
// `op read op://vault/lore/passphrase` or `pass show lore/export`.
func FromCommand(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("secret command is empty")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("secret command failed: %w", err)
	}
	// Trim only the trailing newline tools append, not interior or intentional
	// trailing spaces a passphrase might contain.
	s := strings.TrimRight(string(out), "\r\n")
	if s == "" {
		return "", fmt.Errorf("secret command produced no output")
	}
	return s, nil
}
