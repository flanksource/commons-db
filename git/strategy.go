package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// PickStrategy is how ChangeRequest.CherryPick commits land on the branch.
type PickStrategy string

const (
	// PickCherryPick runs git cherry-pick -x: it records the origin commit and
	// needs history shared with the base. It is the zero value's meaning.
	PickCherryPick PickStrategy = "cherry-pick"
	// PickReplay applies each commit's patch as a new commit (git format-patch
	// | git am --3way), keeping its message, author and date; it needs no
	// shared history and refuses merge commits.
	PickReplay PickStrategy = "replay"
)

func (p PickStrategy) validate() error {
	switch p {
	case "", PickCherryPick, PickReplay:
		return nil
	}
	return fmt.Errorf("pick strategy %q is not one of %q, %q", string(p), PickCherryPick, PickReplay)
}

// replay applies sha's patch onto the branch as a new commit keeping its
// message, author and date, and reports false when its changes were already
// there.
func (w *Worktree) replay(ctx context.Context, env []string, sha string) (bool, error) {
	patch, err := runBytes(ctx, command{dir: w.Dir, args: []string{"format-patch", "-1", "-k", "--stdout", "--binary", "--end-of-options", sha}})
	if err != nil {
		return false, fmt.Errorf("replay %s: %w", sha, err)
	}
	before, err := w.head(ctx)
	if err != nil {
		return false, err
	}
	if _, amErr := run(ctx, command{dir: w.Dir, env: env, stdin: patch, args: []string{"am", "-k", "--3way", "--keep-cr"}}); amErr != nil {
		return false, w.replayFailed(ctx, sha, amErr)
	}
	after, err := w.head(ctx)
	return after != before, err
}

// replayFailed aborts a stopped git am, reporting a conflict by its unmerged
// paths and an empty patch as a commit already applied.
func (w *Worktree) replayFailed(ctx context.Context, sha string, amErr error) error {
	abort := func() error {
		_, err := run(ctx, command{dir: w.Dir, args: []string{"am", "--abort"}})
		return err
	}
	unmerged, err := run(ctx, command{dir: w.Dir, args: []string{"-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=U"}})
	if err != nil {
		return errors.Join(amErr, err, abort())
	}
	if files := lines(unmerged); len(files) > 0 {
		return errors.Join(&CherryPickConflictError{Commit: sha, Files: files}, abort())
	}
	return errors.Join(fmt.Errorf("replay %s: %w", sha, amErr), abort())
}

// squash folds the commits picked onto the base into one: their original
// messages in order, the first one's author and date, a Co-authored-by
// trailer per other author, then request.Trailers.
func (w *Worktree) squash(ctx context.Context, request ChangeRequest, skipped []string) error {
	if !request.Squash {
		return nil
	}
	kept := slices.DeleteFunc(slices.Clone(request.CherryPick), func(sha string) bool { return slices.Contains(skipped, sha) })
	if len(kept) == 0 {
		return nil
	}
	var messages, coAuthors []string
	var author, date string
	for i, sha := range kept {
		out, err := run(ctx, command{dir: w.Dir, args: []string{"show", "-s", "--format=%an <%ae>%x00%aI%x00%B", "--end-of-options", sha}})
		if err != nil {
			return fmt.Errorf("squash %s: %w", sha, err)
		}
		parts := strings.SplitN(out, "\x00", 3)
		if len(parts) != 3 {
			return fmt.Errorf("squash %s: unexpected git show output %q", sha, out)
		}
		messages = append(messages, strings.TrimSpace(parts[2]))
		switch {
		case i == 0:
			author, date = parts[0], parts[1]
		case parts[0] != author && !slices.Contains(coAuthors, "Co-authored-by: "+parts[0]):
			coAuthors = append(coAuthors, "Co-authored-by: "+parts[0])
		}
	}
	message := strings.Join(messages, "\n\n")
	if trailers := append(coAuthors, request.Trailers...); len(trailers) > 0 {
		message += "\n\n" + strings.Join(trailers, "\n")
	}
	env, err := request.Author.committerEnv()
	if err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"reset", "--quiet", "--soft", "--end-of-options", w.Ref.SHA}}); err != nil {
		return err
	}
	_, err = run(ctx, command{dir: w.Dir, env: env, stdin: []byte(message),
		args: []string{"commit", "--quiet", "--no-verify", "--cleanup=whitespace", "--author", author, "--date", date, "-F", "-"}})
	return err
}

// rebase rebases the commits of request.Rebase the base lacks onto the base
// with git rebase, once they share history.
func (w *Worktree) rebase(ctx context.Context, request ChangeRequest) error {
	if request.Rebase == "" {
		return nil
	}
	if err := w.fetchCommits(ctx, request.Source, []string{request.Rebase}); err != nil {
		return err
	}
	base, err := mergeBase(ctx, w.Dir, w.Ref.SHA, request.Rebase)
	if err != nil {
		return fmt.Errorf("rebase %s onto %s: %w", request.Rebase, request.Base, err)
	}
	if base == request.Rebase {
		return fmt.Errorf("change %s has nothing to push: %s is already on %s", request.Branch, request.Rebase, request.Base)
	}
	env, err := request.Author.committerEnv()
	if err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"reset", "--quiet", "--hard", "--end-of-options", request.Rebase}}); err != nil {
		return err
	}
	_, rebaseErr := run(ctx, command{dir: w.Dir, env: env, args: []string{"rebase", "--quiet", "--no-autosquash", "--onto", w.Ref.SHA, base}})
	if rebaseErr == nil {
		return nil
	}
	abort := func() error {
		_, err := run(ctx, command{dir: w.Dir, args: []string{"rebase", "--abort"}})
		return err
	}
	unmerged, err := run(ctx, command{dir: w.Dir, args: []string{"-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=U"}})
	if err != nil {
		return errors.Join(rebaseErr, err, abort())
	}
	if files := lines(unmerged); len(files) > 0 {
		commit, err := run(ctx, command{dir: w.Dir, args: []string{"rev-parse", "--verify", "--quiet", "REBASE_HEAD"}})
		return errors.Join(&CherryPickConflictError{Commit: commit, Files: files}, err, abort())
	}
	return errors.Join(fmt.Errorf("rebase %s onto %s: %w", request.Rebase, request.Base, rebaseErr), abort())
}

func (w *Worktree) head(ctx context.Context) (string, error) {
	return run(ctx, command{dir: w.Dir, args: []string{"rev-parse", "--verify", "HEAD"}})
}
