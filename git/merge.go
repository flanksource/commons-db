package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
)

// MergeCommit is one ChangeRequest.Merge head: a full commit SHA merged onto
// the branch with a merge commit of its own, named by Message (default
// "Merge <sha>").
type MergeCommit struct {
	SHA     string
	Message string
}

// MergeConflictError is a ChangeRequest.Merge head that does not merge
// cleanly onto the branch as the earlier heads left it: the merge was aborted
// and nothing was pushed. Files are the paths left unmerged.
type MergeConflictError struct {
	Commit string
	Files  []string
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merge %s conflicts in %s", e.Commit, strings.Join(e.Files, ", "))
}

func (r ChangeRequest) validateMerge() error {
	if len(r.Merge) == 0 {
		return nil
	}
	if r.Head != "" || r.Rebase != "" || len(r.CherryPick) > 0 || len(r.Files) > 0 || r.Pick != "" || r.Squash || len(r.Trailers) > 0 {
		return errors.New("a merge takes no head, rebase, files, commits to cherry-pick, pick strategy, squash or trailers")
	}
	for _, merge := range r.Merge {
		if !fullSHAPattern.MatchString(merge.SHA) {
			return fmt.Errorf("merge head %q must be a full 40-character lowercase commit SHA", merge.SHA)
		}
	}
	return nil
}

// merge merges request.Merge in order onto the worktree's branch, each with a
// non-fast-forward merge commit by request.Author, refusing a head the
// branch already holds.
func (w *Worktree) merge(ctx context.Context, request ChangeRequest) error {
	if len(request.Merge) == 0 {
		return nil
	}
	shas := make([]string, len(request.Merge))
	for i, merge := range request.Merge {
		shas[i] = merge.SHA
	}
	if err := w.fetchCommits(ctx, request.Source, shas); err != nil {
		return err
	}
	env, err := request.Author.env()
	if err != nil {
		return err
	}
	for _, merge := range request.Merge {
		base, err := mergeBase(ctx, w.Dir, "HEAD", merge.SHA)
		if err != nil {
			return fmt.Errorf("merge %s onto %s: %w", merge.SHA, request.Base, err)
		}
		if base == merge.SHA {
			return fmt.Errorf("merge %s: it is already on %s", merge.SHA, request.Base)
		}
		message := cmp.Or(merge.Message, "Merge "+merge.SHA)
		_, mergeErr := run(ctx, command{dir: w.Dir, env: append(baseEnv(), env...),
			args: []string{"merge", "--no-ff", "--no-verify", "-m", message, "--end-of-options", merge.SHA}})
		if mergeErr != nil {
			return w.mergeFailed(ctx, merge.SHA, mergeErr)
		}
	}
	return nil
}

// mergeFailed aborts a stopped merge, classifying a conflict by its unmerged
// paths.
func (w *Worktree) mergeFailed(ctx context.Context, sha string, mergeErr error) error {
	// -z keeps each path exactly as committed: line output would quote tabs and
	// backslashes, and splitting it into lines would trim leading spaces.
	unmerged, err := runBytes(ctx, command{dir: w.Dir, args: []string{"diff", "--name-only", "--diff-filter=U", "-z"}})
	_, abortErr := run(ctx, command{dir: w.Dir, args: []string{"merge", "--abort"}})
	if err != nil {
		return errors.Join(fmt.Errorf("merge %s: %w", sha, mergeErr), err, abortErr)
	}
	if listed := strings.TrimSuffix(string(unmerged), "\x00"); listed != "" {
		files := strings.Split(listed, "\x00")
		return errors.Join(&MergeConflictError{Commit: sha, Files: files}, abortErr)
	}
	return errors.Join(fmt.Errorf("merge %s: %w", sha, mergeErr), abortErr)
}

// DeleteBranch deletes branch from repo's remote only while it still points
// at expectedSHA (--force-with-lease), so a branch moved since is kept and
// reported.
func (c *Cache) DeleteBranch(ctx context.Context, repo *Repository, branch, expectedSHA string) (err error) {
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	if !fullSHAPattern.MatchString(expectedSHA) {
		return fmt.Errorf("delete branch %s: expected SHA %q must be a full 40-character lowercase commit SHA", branch, expectedSHA)
	}
	paths, err := c.mirrorPaths(repo)
	if err != nil {
		return err
	}
	ctx, unlock, err := paths.lockWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := paths.ensure(ctx, repo); err != nil {
		return err
	}
	env, cleanup, err := commandEnv(repo)
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"push", "--quiet", "--force-with-lease=refs/heads/" + branch + ":" + expectedSHA, "--end-of-options", repo.URL.Clone, ":refs/heads/" + branch}
	if _, err := run(ctx, command{dir: paths.git, env: env, args: args}); err != nil {
		return fmt.Errorf("delete branch %s at %s from %s: %w", branch, expectedSHA, repo.URL.Redacted(), err)
	}
	return nil
}
