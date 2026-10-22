package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CherryPickConflictError is a ChangeRequest.CherryPick commit that does not
// apply cleanly onto the base: the cherry-pick was aborted and nothing was
// pushed. Files are the paths left unmerged.
type CherryPickConflictError struct {
	Commit string
	Files  []string
}

func (e *CherryPickConflictError) Error() string {
	return fmt.Sprintf("cherry-pick %s conflicts in %s", e.Commit, strings.Join(e.Files, ", "))
}

// CherryPickEmptyError is a ChangeRequest.CherryPick commit whose changes are
// already on Base (its patch was applied there before): the cherry-pick was
// aborted and nothing was pushed.
type CherryPickEmptyError struct {
	Commit string
	Base   string
}

func (e *CherryPickEmptyError) Error() string {
	return fmt.Sprintf("cherry-pick %s is empty: its changes are already on %s (set AllowEmpty to skip it)", e.Commit, e.Base)
}

// cherryPick lands request.CherryPick in order onto the worktree's branch by
// request.Pick, appending request.Trailers to each landed commit unless they
// are squashed, and returns the commits skipped as already applied.
func (w *Worktree) cherryPick(ctx context.Context, request ChangeRequest) ([]string, error) {
	if len(request.CherryPick) == 0 {
		return nil, nil
	}
	if err := w.fetchCommits(ctx, request.Source, request.CherryPick); err != nil {
		return nil, err
	}
	merges, err := w.mergeCommits(ctx, request.CherryPick, request.CherryPickMainline)
	if err != nil {
		return nil, err
	}
	for sha := range merges {
		if request.Pick == PickReplay {
			return nil, fmt.Errorf("replay %s is a merge commit: a replay applies single-parent patches only", sha)
		}
	}
	env, err := request.Author.committerEnv()
	if err != nil {
		return nil, err
	}
	trailers := request.Trailers
	if request.Squash {
		trailers = nil
	}
	var skipped []string
	for _, sha := range request.CherryPick {
		landed, err := w.pickOne(ctx, pickOptions{Env: env, Request: request, SHA: sha, Merge: merges[sha]})
		if err != nil {
			return nil, err
		}
		if !landed {
			skipped = append(skipped, sha)
			continue
		}
		if err := w.appendTrailers(ctx, env, trailers); err != nil {
			return nil, fmt.Errorf("cherry-pick %s: %w", sha, err)
		}
	}
	return skipped, nil
}

// pickOptions is one commit SHA to land by Request.Pick, committing as Env's
// committer; Merge marks a merge commit replayed against its mainline.
type pickOptions struct {
	Env     []string
	Request ChangeRequest
	SHA     string
	Merge   bool
}

// pickOne lands options.SHA and reports false when it was skipped as already
// applied, which only AllowEmpty permits.
func (w *Worktree) pickOne(ctx context.Context, options pickOptions) (bool, error) {
	env, request, sha := options.Env, options.Request, options.SHA
	if request.Pick == PickReplay {
		landed, err := w.replay(ctx, env, sha)
		if err == nil && !landed && !request.AllowEmpty {
			return false, &CherryPickEmptyError{Commit: sha, Base: request.Base}
		}
		return landed, err
	}
	args := []string{"cherry-pick", "-x"}
	if options.Merge {
		args = append(args, "-m", strconv.Itoa(request.CherryPickMainline))
	}
	_, pickErr := run(ctx, command{dir: w.Dir, env: env, args: append(args, "--end-of-options", sha)})
	if pickErr == nil {
		return true, nil
	}
	return false, w.cherryPickFailed(ctx, sha, request, pickErr)
}

// fetchCommits fetches, by SHA from source (nil is the worktree's own
// repository), every commit the mirror does not have yet.
func (w *Worktree) fetchCommits(ctx context.Context, source *Repository, shas []string) (err error) {
	source = cmp.Or(source, w.repo)
	lock, err := acquire(ctx, w.mirrorLock, "mirror lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	for _, sha := range shas {
		if hasCommit(ctx, w.mirror, sha) {
			continue
		}
		if _, err := fetch(ctx, fetchOptions{Dir: w.mirror, Owner: w.repo, Repo: source, Ref: Ref{Name: sha, FullName: sha, SHA: sha, Kind: RefSHA}}); err != nil {
			return fmt.Errorf("cherry-pick commit %s: %w", sha, err)
		}
	}
	return nil
}

// mergeCommits reports which commits are merges, refusing every merge before
// anything is replayed unless mainline names one of its parents.
func (w *Worktree) mergeCommits(ctx context.Context, shas []string, mainline int) (map[string]bool, error) {
	merges := map[string]bool{}
	for _, sha := range shas {
		out, err := run(ctx, command{dir: w.Dir, args: []string{"rev-list", "--parents", "--max-count=1", "--end-of-options", sha}})
		if err != nil {
			return nil, err
		}
		parents := len(strings.Fields(out)) - 1
		if parents < 2 {
			continue
		}
		if mainline < 1 || mainline > parents {
			return nil, fmt.Errorf("cherry-pick %s is a merge commit with %d parents and needs a mainline: set CherryPickMainline to the parent (1-%d) to replay it against",
				sha, parents, parents)
		}
		merges[sha] = true
	}
	return merges, nil
}

// cherryPickFailed classifies a failed cherry-pick: a conflict or any other
// failure aborts it, an empty one (already applied) is skipped only when
// allowEmpty is set.
func (w *Worktree) cherryPickFailed(ctx context.Context, sha string, request ChangeRequest, pickErr error) error {
	unmerged, err := run(ctx, command{dir: w.Dir, args: []string{"-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=U"}})
	if err != nil {
		return errors.Join(pickErr, err, w.abortCherryPick(ctx))
	}
	if files := lines(unmerged); len(files) > 0 {
		return errors.Join(&CherryPickConflictError{Commit: sha, Files: files}, w.abortCherryPick(ctx))
	}
	if !w.cherryPickEmpty(ctx) {
		return errors.Join(fmt.Errorf("cherry-pick %s: %w", sha, pickErr), w.abortCherryPick(ctx))
	}
	if !request.AllowEmpty {
		return errors.Join(&CherryPickEmptyError{Commit: sha, Base: request.Base}, w.abortCherryPick(ctx))
	}
	_, err = run(ctx, command{dir: w.Dir, args: []string{"cherry-pick", "--skip"}})
	return err
}

// appendTrailers amends the commit just replayed to carry trailers, keeping
// its author and committing as env's committer.
func (w *Worktree) appendTrailers(ctx context.Context, env []string, trailers []string) error {
	if len(trailers) == 0 {
		return nil
	}
	args := []string{"commit", "--amend", "--no-edit", "--no-verify"}
	for _, trailer := range trailers {
		args = append(args, "--trailer", trailer)
	}
	_, err := run(ctx, command{dir: w.Dir, env: env, args: args})
	return err
}

// cherryPickEmpty reports a stopped cherry-pick that staged nothing.
func (w *Worktree) cherryPickEmpty(ctx context.Context) bool {
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"rev-parse", "--verify", "--quiet", "CHERRY_PICK_HEAD"}}); err != nil {
		return false
	}
	_, err := run(ctx, command{dir: w.Dir, args: []string{"diff", "--cached", "--quiet"}})
	return err == nil
}

func (w *Worktree) abortCherryPick(ctx context.Context) error {
	_, err := run(ctx, command{dir: w.Dir, args: []string{"cherry-pick", "--abort"}})
	return err
}
