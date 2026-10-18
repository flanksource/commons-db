package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoIdentity marks a process whose global git config lacks user.name or
// user.email.
var ErrNoIdentity = errors.New("no git user.name/user.email configured")

// DefaultIdentity reads user.name and user.email from the calling process's
// own global git config, as the git binary resolves it from the process
// environment (HOME, XDG_CONFIG_HOME, GIT_CONFIG_GLOBAL). Every other command
// in this package runs with the global config isolated (baseEnv); this is the
// one read of it, so a server can author a commit as itself. A missing name
// or email is ErrNoIdentity.
func DefaultIdentity(ctx context.Context) (Signature, error) {
	name, err := globalConfigValue(ctx, "user.name")
	if err != nil {
		return Signature{}, err
	}
	email, err := globalConfigValue(ctx, "user.email")
	if err != nil {
		return Signature{}, err
	}
	if name == "" || email == "" {
		return Signature{}, fmt.Errorf("%w: user.name=%q user.email=%q", ErrNoIdentity, name, email)
	}
	return Signature{Name: name, Email: email}, nil
}

// globalConfigValue is key in the global git config, empty when it is unset
// (git config --get exits 1 for exactly that).
func globalConfigValue(ctx context.Context, key string) (string, error) {
	if err := requireGit(ctx); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "config", "--global", "--get", key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("git config --global --get %s: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
