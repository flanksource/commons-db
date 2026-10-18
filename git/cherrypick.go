package git

import (
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

// cherryPick replays request.CherryPick in order onto the worktree's branch
// with git cherry-pick -x and returns the commits skipped as already applied.
func (w *Worktree) cherryPick(ctx context.Context, request ChangeRequest) ([]string, error) {
	if len(request.CherryPick) == 0 {
		return nil, nil
	}
	if err := w.fetchCommits(ctx, request.CherryPick); err != nil {
		return nil, err
	}
	merges, err := w.mergeCommits(ctx, request.CherryPick, request.CherryPickMainline)
	if err != nil {
		return nil, err
	}
	env, err := request.Author.committerEnv()
	if err != nil {
		return nil, err
	}
	var skipped []string
	for _, sha := range request.CherryPick {
		args := []string{"cherry-pick", "-x"}
		if merges[sha] {
			args = append(args, "-m", strconv.Itoa(request.CherryPickMainline))
		}
		_, pickErr := run(ctx, command{dir: w.Dir, env: env, args: append(args, "--end-of-options", sha)})
		if pickErr == nil {
			continue
		}
		if err := w.cherryPickFailed(ctx, sha, request.AllowEmpty, pickErr); err != nil {
			return nil, err
		}
		skipped = append(skipped, sha)
	}
	return skipped, nil
}

// fetchCommits fetches, by SHA, every commit the mirror does not have yet.
func (w *Worktree) fetchCommits(ctx context.Context, shas []string) (err error) {
	lock, err := acquire(ctx, w.mirrorLock, "mirror lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	for _, sha := range shas {
		if hasCommit(ctx, w.mirror, sha) {
			continue
		}
		if _, err := fetch(ctx, w.mirror, w.repo, Ref{Name: sha, FullName: sha, SHA: sha, Kind: RefSHA}, 0); err != nil {
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
func (w *Worktree) cherryPickFailed(ctx context.Context, sha string, allowEmpty bool, pickErr error) error {
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
	if !allowEmpty {
		return errors.Join(fmt.Errorf("cherry-pick %s is empty: its changes are already on the base (set AllowEmpty to skip it)", sha),
			w.abortCherryPick(ctx))
	}
	_, err = run(ctx, command{dir: w.Dir, args: []string{"cherry-pick", "--skip"}})
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
