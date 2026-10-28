package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/google/uuid"
)

// gitCmd is one git invocation in Dir. Env is appended to the process
// environment; Stdin, when set, is fed to the command.
type gitCmd struct {
	Dir   string
	Env   []string
	Stdin []byte
}

func (g gitCmd) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := osExec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	if len(g.Env) > 0 {
		cmd.Env = append(os.Environ(), g.Env...)
	}
	if g.Stdin != nil {
		cmd.Stdin = bytes.NewReader(g.Stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), g.Dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (g gitCmd) string(ctx context.Context, args ...string) (string, error) {
	out, err := g.output(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

func gitString(ctx context.Context, dir string, args ...string) (string, error) {
	return gitCmd{Dir: dir}.string(ctx, args...)
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitCmd{Dir: dir}.output(ctx, args...)
}

// headCommit is HEAD's commit, or "" on an unborn branch.
func headCommit(ctx context.Context, dir string) (string, error) {
	return gitOptional(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
}

// headBranch is the branch HEAD is on — born or not — or "" when detached.
func headBranch(ctx context.Context, dir string) (string, error) {
	return gitOptional(ctx, dir, "symbolic-ref", "--short", "--quiet", "HEAD")
}

// gitOptional runs a --quiet query whose exit status 1 means "no such value"
// (an unresolvable name, a detached HEAD) and returns "" for it; any other
// failure — not a repository, corruption — is a real error.
func gitOptional(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := osExec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exitErr *osExec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// splitNUL splits `-z` output into its non-empty records.
func splitNUL(out []byte) []string {
	var records []string
	for _, record := range strings.Split(string(out), "\x00") {
		if record != "" {
			records = append(records, record)
		}
	}
	return records
}

// withTempIndex runs fn against a private index file inside dir's git
// directory, so a tree can be built from the working state without touching
// the real index. The file is removed afterwards.
func withTempIndex(ctx context.Context, dir string, fn func(git gitCmd, index string) error) error {
	gitDir, err := gitString(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	index := filepath.Join(gitDir, "workspace-index-"+uuid.NewString())
	defer func() { _ = os.Remove(index) }()
	return fn(gitCmd{Dir: dir, Env: []string{"GIT_INDEX_FILE=" + index, "GIT_LITERAL_PATHSPECS=1"}}, index)
}
