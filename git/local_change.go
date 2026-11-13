package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	gitcommand "github.com/flanksource/commons-db/git/internal/command"
)

// LocalCherryPickOptions replays CherryPick (full SHAs), in order, onto Base
// (any revision) inside the local repository at Dir. Mainline is the 1-based
// parent a merge commit is replayed against.
type LocalCherryPickOptions struct {
	Dir        string
	Base       string
	CherryPick []string
	Mainline   int
}

// LocalPushOptions pushes Commit (a full SHA) of the local repository at Dir
// to Branch of the configured Remote.
type LocalPushOptions struct {
	Dir    string
	Remote string
	Commit string
	Branch string
}

// CherryPickLocal replays the commits without a worktree or index: each one is
// merged with git merge-tree and written with git commit-tree into Dir's own
// object store, keeping its message, author and author date, under the
// caller's git configuration (committer identity included). It returns the
// replayed tip; no ref is created or moved. A conflict is
// *CherryPickConflictError and a commit already on the tip *CherryPickEmptyError.
func CherryPickLocal(ctx context.Context, options LocalCherryPickOptions) (string, error) {
	if options.Dir == "" {
		return "", errors.New("local cherry-pick needs the repository directory")
	}
	if len(options.CherryPick) == 0 {
		return "", errors.New("local cherry-pick needs at least one commit")
	}
	for _, sha := range options.CherryPick {
		if !fullSHAPattern.MatchString(sha) {
			return "", fmt.Errorf("cherry-pick %q must be a full 40-character lowercase commit SHA", sha)
		}
	}
	tip, err := runLocal(ctx, options.Dir, nil, "rev-parse", "--verify", "--end-of-options", options.Base+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cherry-pick base %q: %w", options.Base, err)
	}
	for _, sha := range options.CherryPick {
		if tip, err = pickLocal(ctx, options, tip, sha); err != nil {
			return "", err
		}
	}
	return tip, nil
}

func pickLocal(ctx context.Context, options LocalCherryPickOptions, tip, sha string) (string, error) {
	parent, err := pickParent(ctx, options, sha)
	if err != nil {
		return "", err
	}
	tree, err := mergeTreeLocal(ctx, options.Dir, "--merge-base="+parent, tip, sha)
	if err != nil {
		var conflict *CherryPickConflictError
		if errors.As(err, &conflict) {
			conflict.Commit = sha
		}
		return "", err
	}
	tipTree, err := runLocal(ctx, options.Dir, nil, "rev-parse", "--verify", "--end-of-options", tip+"^{tree}")
	if err != nil {
		return "", err
	}
	if tree == tipTree {
		return "", &CherryPickEmptyError{Commit: sha, Base: options.Base}
	}
	meta, err := runLocal(ctx, options.Dir, nil, "show", "-s", "--format=%an%x00%ae%x00%ad%x00%B", "--date=raw", "--end-of-options", sha)
	if err != nil {
		return "", err
	}
	fields := strings.SplitN(meta, "\x00", 4)
	if len(fields) != 4 {
		return "", fmt.Errorf("read commit %s: unexpected metadata %q", sha, meta)
	}
	author := []string{"GIT_AUTHOR_NAME=" + fields[0], "GIT_AUTHOR_EMAIL=" + fields[1], "GIT_AUTHOR_DATE=" + fields[2]}
	return runLocal(ctx, options.Dir, &localInput{env: author, stdin: []byte(fields[3] + "\n")},
		"commit-tree", "-p", tip, "-F", "-", "--end-of-options", tree)
}

// pickParent is what sha's changes are taken against: its only parent, the
// mainline one of a merge commit, or the empty tree of a root commit.
func pickParent(ctx context.Context, options LocalCherryPickOptions, sha string) (string, error) {
	out, err := runLocal(ctx, options.Dir, nil, "rev-list", "--parents", "--max-count=1", "--end-of-options", sha)
	if err != nil {
		return "", fmt.Errorf("cherry-pick commit %s: %w", sha, err)
	}
	parents := strings.Fields(out)[1:]
	switch {
	case len(parents) == 0 && options.Mainline <= 1:
		return runLocal(ctx, options.Dir, &localInput{stdin: []byte{}}, "hash-object", "-t", "tree", "--stdin")
	case len(parents) == 0:
		return "", fmt.Errorf("cherry-pick %s is a root commit: mainline %d does not apply", sha, options.Mainline)
	case len(parents) == 1 && options.Mainline <= 1:
		return parents[0], nil
	case len(parents) == 1:
		return "", fmt.Errorf("cherry-pick %s is not a merge commit: mainline %d does not apply", sha, options.Mainline)
	case options.Mainline < 1 || options.Mainline > len(parents):
		return "", fmt.Errorf("cherry-pick %s is a merge commit with %d parents and needs a mainline (1-%d) to replay it against",
			sha, len(parents), len(parents))
	}
	return parents[options.Mainline-1], nil
}

// mergeTreeLocal writes the merged tree, or reports the conflicted paths.
func mergeTreeLocal(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := gitcommand.Run(ctx, gitcommand.Options{Directory: dir, Environment: localEnv(nil),
		Args: append([]string{"merge-tree", "--write-tree", "--name-only", "--no-messages"}, args...)})
	output := lines(strings.TrimRight(string(result.Output), "\n"))
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(output) > 1 {
		return "", &CherryPickConflictError{Files: output[1:]}
	}
	if err != nil {
		return "", fmt.Errorf("git merge-tree %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr))
	}
	if len(output) == 0 {
		return "", errors.New("git merge-tree wrote no tree")
	}
	return output[0], nil
}

// PushLocal pushes the commit with the caller's git configuration, so the
// remote's own URL and credential helper apply. It never forces.
func PushLocal(ctx context.Context, options LocalPushOptions) error {
	if !fullSHAPattern.MatchString(options.Commit) {
		return fmt.Errorf("push commit %q must be a full 40-character lowercase commit SHA", options.Commit)
	}
	if err := ValidateBranchName(options.Branch); err != nil {
		return err
	}
	if strings.TrimSpace(options.Remote) == "" || strings.HasPrefix(options.Remote, "-") {
		return fmt.Errorf("invalid push remote %q", options.Remote)
	}
	if _, err := runLocal(ctx, options.Dir, nil, "remote", "get-url", "--end-of-options", options.Remote); err != nil {
		return fmt.Errorf("push remote %q: %w", options.Remote, err)
	}
	if _, err := runLocal(ctx, options.Dir, nil, "push", "--quiet", "--end-of-options", options.Remote,
		options.Commit+":refs/heads/"+options.Branch); err != nil {
		return fmt.Errorf("push %s to %s/%s: %w", options.Commit[:12], options.Remote, options.Branch, err)
	}
	return nil
}

type localInput struct {
	env   []string
	stdin []byte
}

func runLocal(ctx context.Context, dir string, input *localInput, args ...string) (string, error) {
	c := command{dir: dir, args: args}
	if input != nil {
		c.env, c.stdin = localEnv(input.env), input.stdin
	} else {
		c.env = localEnv(nil)
	}
	return run(ctx, c)
}

// localSteering are the variables that would point git at another
// repository, index or object store than dir's.
var localSteering = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OBJECT_DIRECTORY=",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_COMMON_DIR=", "GIT_NAMESPACE=", "GIT_PREFIX=", "LC_ALL="}

// localEnv is the caller's environment, with its git configuration and
// identity, minus repository steering, in a stable locale, plus extra.
func localEnv(extra []string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		steering := false
		for _, prefix := range localSteering {
			steering = steering || strings.HasPrefix(entry, prefix)
		}
		if !steering {
			env = append(env, entry)
		}
	}
	return append(append(env, "LC_ALL=C"), extra...)
}
