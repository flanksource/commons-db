package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const lockRetry = 50 * time.Millisecond

// Cache keeps one bare mirror per repository and worktrees per ref:
//
//	<Root>/<RepoURL.CacheKey>/mirror.git
//	<Root>/<RepoURL.CacheKey>/mirror.lock
//	<Root>/<RepoURL.CacheKey>/worktrees/<ref dir>[.lock]
//
// mirror.lock serializes fetches and worktree bookkeeping; each worktree is
// leased exclusively until Release, so concurrent checkouts of different refs
// proceed while a second checkout of the same ref waits.
type Cache struct {
	Root string
}

// CheckoutOptions configures Cache.Checkout. Ref is a branch, tag, full ref
// or 40-char SHA (empty is the remote HEAD). An Ephemeral worktree gets a
// unique directory removed on Release.
type CheckoutOptions struct {
	Ref       string
	Depth     int
	Ephemeral bool
}

// Checkout leases a worktree at ref. A commit already in the mirror is not
// fetched again.
func (c *Cache) Checkout(ctx context.Context, repo *Repository, options CheckoutOptions) (*Worktree, error) {
	if c.Root == "" {
		return nil, errors.New("git cache root is required")
	}
	ref, err := repo.Resolve(ctx, options.Ref)
	if err != nil {
		return nil, err
	}
	return c.checkoutRef(ctx, repo, ref, options)
}

// checkoutRef leases a worktree at an already resolved ref, so a caller that
// checked ref.SHA checks out exactly that commit (a fetch can still advance
// it when the mirror lacks the commit; the caller compares Worktree.Ref.SHA).
func (c *Cache) checkoutRef(ctx context.Context, repo *Repository, ref Ref, options CheckoutOptions) (*Worktree, error) {
	if c.Root == "" {
		return nil, errors.New("git cache root is required")
	}
	base := filepath.Join(c.Root, repo.URL.CacheKey())
	name, err := worktreeName(ref, options.Ephemeral)
	if err != nil {
		return nil, err
	}
	worktree := &Worktree{Dir: filepath.Join(base, "worktrees", name), Ref: ref, repo: repo,
		mirror: filepath.Join(base, "mirror.git"), mirrorLock: filepath.Join(base, "mirror.lock"), ephemeral: options.Ephemeral}
	if worktree.lease, err = acquire(ctx, worktree.Dir+".lock", "worktree lease"); err != nil {
		return nil, fmt.Errorf("%s at %s: %w", repo.URL.Redacted(), ref.Name, err)
	}
	if err := worktree.materialize(ctx, options.Depth); err != nil {
		return nil, errors.Join(err, worktree.unlease())
	}
	return worktree, nil
}

func worktreeName(ref Ref, ephemeral bool) (string, error) {
	name := refDirName(ref.FullName)
	if !ephemeral {
		return name, nil
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate ephemeral worktree name: %w", err)
	}
	return name + "-" + hex.EncodeToString(suffix), nil
}

// materialize fetches ref into the mirror unless its commit is cached, then
// adds (or resets) the worktree at that commit, under the mirror lock.
func (w *Worktree) materialize(ctx context.Context, depth int) (err error) {
	lock, err := acquire(ctx, w.mirrorLock, "mirror lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, w.mirror); err != nil {
		return err
	}
	if !hasCommit(ctx, w.mirror, w.Ref.SHA) {
		if w.Ref.SHA, err = fetch(ctx, fetchOptions{Dir: w.mirror, Owner: w.repo, Repo: w.repo, Ref: w.Ref, Depth: depth}); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err == nil {
		return w.reset(ctx)
	}
	if _, err := run(ctx, command{dir: w.mirror, args: []string{"worktree", "prune"}}); err != nil {
		return err
	}
	_, err = run(ctx, command{dir: w.mirror, args: []string{"worktree", "add", "--quiet", "--force", "--detach",
		"--end-of-options", w.Dir, w.Ref.SHA}})
	return err
}

func (w *Worktree) reset(ctx context.Context) error {
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"checkout", "--quiet", "--force", "--detach",
		"--end-of-options", w.Ref.SHA}}); err != nil {
		return err
	}
	_, err := run(ctx, command{dir: w.Dir, args: []string{"clean", "-ffdxq"}})
	return err
}

func ensureMirror(ctx context.Context, mirror string) error {
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		return fmt.Errorf("create git mirror %s: %w", mirror, err)
	}
	_, err := run(ctx, command{dir: mirror, args: []string{"init", "--quiet", "--bare"}})
	return err
}

func hasCommit(ctx context.Context, dir, sha string) bool {
	if !fullSHAPattern.MatchString(sha) {
		return false
	}
	_, err := run(ctx, command{dir: dir, args: []string{"cat-file", "-e", "--end-of-options", sha + "^{commit}"}})
	return err == nil
}

func (c *Cache) Files(ctx context.Context, repo *Repository, sha string) ([]string, error) {
	if !fullSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("list files: a full 40-character commit SHA is required")
	}
	paths := []string{}
	err := c.withMirror(ctx, repo, func(mirror string) error {
		if !hasCommit(ctx, mirror, sha) {
			if _, err := fetch(ctx, fetchOptions{Dir: mirror, Owner: repo, Repo: repo, Ref: Ref{Name: sha, FullName: sha, SHA: sha, Kind: RefSHA}}); err != nil {
				return err
			}
		}
		output, err := run(ctx, command{dir: mirror, args: []string{"ls-tree", "-r", "-z", "--full-tree", "--end-of-options", sha}})
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
			if entry == "" {
				continue
			}
			metadata, path, ok := strings.Cut(entry, "\t")
			fields := strings.Fields(metadata)
			if !ok || len(fields) != 3 {
				return fmt.Errorf("unexpected Git tree entry %q", entry)
			}
			if fields[1] == "blob" && (fields[0] == "100644" || fields[0] == "100755") {
				paths = append(paths, path)
			}
		}
		return nil
	})
	return paths, err
}

// acquire takes an exclusive file lock, waiting until ctx is done.
func acquire(ctx context.Context, path, what string) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create %s directory: %w", what, err)
	}
	lock := flock.New(path)
	locked, err := lock.TryLockContext(ctx, lockRetry)
	if err != nil || !locked {
		return nil, fmt.Errorf("acquire %s %s: %w", what, path, errors.Join(err, ctx.Err()))
	}
	return lock, nil
}
