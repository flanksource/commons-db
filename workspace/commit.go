package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// CommitMode is how Commit records the staged change.
type CommitMode string

const (
	CommitNew   CommitMode = "commit"
	CommitFixup CommitMode = "fixup"
	CommitAmend CommitMode = "amend"
)

// ErrNothingStaged is returned when, after excludes are dropped, there is
// nothing to commit.
var ErrNothingStaged = errors.New("workspace: nothing staged to commit")

// Signature is a commit author.
type Signature struct{ Name, Email string }

// CommitOptions configures Commit.
type CommitOptions struct {
	// Paths are staged before committing; nil stages every Change. Excluded
	// paths are always dropped.
	Paths []string
	Mode  CommitMode
	// Anchor is the commit a fixup targets.
	Anchor string
	// Message is required for a new commit; an amend keeps the old message
	// when it is empty.
	Message  string
	Trailers []string
	// Author overrides the configured identity for author and committer.
	Author *Signature
	Hooks  []Hook
	Gates  []Gate
	// GitHooks runs the repository's own git hooks; off by default because the
	// workspace pipeline is the hook/gate mechanism.
	GitHooks bool
}

// CommitResult is the commit Commit made and the paths it carried.
type CommitResult struct {
	SHA   string
	Paths []string
}

// Commit stages opts.Paths, runs the hooks and restages, runs the gates over
// the final staged set, and commits. A refusing gate leaves HEAD untouched.
func (w *Workspace) Commit(ctx context.Context, opts CommitOptions) (*CommitResult, error) {
	args, err := commitArgs(opts)
	if err != nil {
		return nil, err
	}
	paths := opts.Paths
	if paths == nil {
		if paths, err = w.Changes(ctx); err != nil {
			return nil, err
		}
	}
	paths = w.withoutExcluded(paths)
	if err := w.stage(ctx, paths); err != nil {
		return nil, err
	}
	for _, hook := range opts.Hooks {
		if err := hook.Run(ctx, w, paths); err != nil {
			return nil, fmt.Errorf("workspace: %s hook: %w", hook.Name(), err)
		}
	}
	if len(opts.Hooks) > 0 {
		if err := w.stage(ctx, paths); err != nil {
			return nil, err
		}
	}

	head, err := headCommit(ctx, w.Dir)
	if err != nil {
		return nil, err
	}
	changes, err := w.stagedChanges(ctx, head)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 && opts.Mode != CommitAmend {
		return nil, ErrNothingStaged
	}
	staged := make([]string, 0, len(changes))
	for _, change := range changes {
		staged = append(staged, change.Path)
	}
	if err := w.runGates(ctx, Check{Phase: PhaseCommit, From: head, Paths: staged, Changes: changes}, opts.Gates); err != nil {
		return nil, err
	}

	git := gitCmd{Dir: w.Dir}
	if a := opts.Author; a != nil {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Email) == "" {
			return nil, errors.New("workspace: commit author needs a name and an email")
		}
		git.Env = []string{"GIT_AUTHOR_NAME=" + a.Name, "GIT_AUTHOR_EMAIL=" + a.Email,
			"GIT_COMMITTER_NAME=" + a.Name, "GIT_COMMITTER_EMAIL=" + a.Email}
	}
	if !opts.GitHooks {
		args = append(args, "--no-verify")
	}
	if _, err := git.output(ctx, args...); err != nil {
		return nil, err
	}
	sha, err := gitString(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	return &CommitResult{SHA: sha, Paths: staged}, nil
}

func commitArgs(opts CommitOptions) ([]string, error) {
	args := []string{"commit", "--quiet"}
	switch opts.Mode {
	case "", CommitNew:
		if strings.TrimSpace(opts.Message) == "" {
			return nil, errors.New("workspace: a commit needs a Message")
		}
		args = append(args, "-m", opts.Message)
	case CommitFixup:
		if opts.Anchor == "" {
			return nil, errors.New("workspace: a fixup commit needs an Anchor")
		}
		args = append(args, "--fixup="+opts.Anchor)
	case CommitAmend:
		args = append(args, "--amend")
		if opts.Message == "" {
			args = append(args, "--no-edit")
		} else {
			args = append(args, "-m", opts.Message)
		}
	default:
		return nil, fmt.Errorf("workspace: unknown commit mode %q", opts.Mode)
	}
	for _, trailer := range opts.Trailers {
		args = append(args, "--trailer", trailer)
	}
	return args, nil
}

// stage adds paths — present, modified or deleted — to the index as literal
// pathspecs, so a name with glob characters is never expanded. A path on
// neither disk nor the index is already staged as a deletion (`git rm`) and is
// left as it is: `git add` would reject it as matching nothing.
func (w *Workspace) stage(ctx context.Context, paths []string) error {
	paths, err := w.stageable(ctx, paths)
	if err != nil || len(paths) == 0 {
		return err
	}
	_, err = gitCmd{Dir: w.Dir, Env: []string{"GIT_LITERAL_PATHSPECS=1"}, Stdin: []byte(strings.Join(paths, "\x00"))}.
		output(ctx, "add", "--all", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

func (w *Workspace) stageable(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out, err := gitCmd{Dir: w.Dir, Env: []string{"GIT_LITERAL_PATHSPECS=1"}}.
		output(ctx, append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	indexed := map[string]struct{}{}
	for _, path := range splitNUL(out) {
		indexed[path] = struct{}{}
	}
	keep := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := indexed[path]; ok {
			keep = append(keep, path)
			continue
		}
		if _, err := os.Lstat(filepath.Join(w.Dir, path)); err == nil {
			keep = append(keep, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return keep, nil
}

// stagedChanges is the index against head (or the empty tree before the first
// commit), with renames split into a deletion and an addition so gates see
// both names.
func (w *Workspace) stagedChanges(ctx context.Context, head string) ([]Change, error) {
	base := head
	if base == "" {
		var err error
		if base, err = emptyTree(ctx, w.Dir); err != nil {
			return nil, err
		}
	}
	out, err := gitOutput(ctx, w.Dir, "diff-index", "--cached", "-z", "--name-status", "--no-renames", base)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out, "index.."+base)
}
