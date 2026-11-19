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

	"github.com/flanksource/commons-db/lease"
	"github.com/gofrs/flock"
)

const lockRetry = 50 * time.Millisecond

// Cache leases worktrees from per-repository bare mirrors. GitHub and Azure
// DevOps mirrors borrow complete objects from an organization pool.
//
//	<Root>/v2/<RepoURL.CacheKey>/mirror.git
//	<Root>/v2/<RepoURL.CacheKey>/mirror.lock
//	<Root>/v2/<RepoURL.CacheKey>/pins/<sha>
//	<Root>/v2/<RepoURL.CacheKey>/worktrees/<ref dir>[.lock]
//
// mirror.lock is held shared by reads and exclusively by fetches, pin writes,
// Maintain and worktree bookkeeping; a pin file's mtime is when a read last
// used that commit (see Maintain). Each worktree is
// leased exclusively until Release, so concurrent checkouts of different refs
// proceed while a second checkout of the same ref waits.
//
// Every cached commit is complete: a read or checkout of a missing commit
// fetches it with its whole history, into the organization pool for a pooled
// mirror (stored once, see fetchIntoPool) and into the mirror otherwise. No
// read creates a shallow mirror, and a shallow one is refused.
//
// Organization pools live under PoolRoot when it is set (a volume every cache
// sharing them mounts at the same path), else under <Root>/v2/pools. Pool
// locks are leases from Locker, which a shared PoolRoot requires; without
// one a local pool uses flock files beside it (lease.NewFile, scope
// "git-pool"). Pool maintenance removes a repository's objects only once no
// cache has pinned, fetched or reconciled it for PoolRetain (30 days by
// default), so every cache sharing a pool must run Maintain more often.
// Holder names this process in lease reports: POD_NAME, else
// <hostname>-<pid>, when empty.
type Cache struct {
	Root       string
	PoolRoot   string
	Locker     lease.Locker
	PoolRetain time.Duration
	Holder     string
}

// CheckoutOptions configures Cache.Checkout. Ref is a branch, tag, full ref
// or 40-char SHA (empty is the remote HEAD). An Ephemeral worktree gets a
// unique directory removed on Release.
type CheckoutOptions struct {
	Ref       string
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

// checkoutRef leases a worktree at the exact commit of an already resolved ref.
func (c *Cache) checkoutRef(ctx context.Context, repo *Repository, ref Ref, options CheckoutOptions) (*Worktree, error) {
	if c.Root == "" {
		return nil, errors.New("git cache root is required")
	}
	paths, err := c.mirrorPaths(repo)
	if err != nil {
		return nil, err
	}
	name, err := worktreeName(ref, options.Ephemeral)
	if err != nil {
		return nil, err
	}
	worktree := &Worktree{Dir: filepath.Join(paths.dir, "worktrees", name), Ref: ref, repo: repo,
		paths: paths, ephemeral: options.Ephemeral}
	if worktree.lease, err = acquire(ctx, worktree.Dir+".lock", "worktree lease", exclusiveLock); err != nil {
		return nil, fmt.Errorf("%s at %s: %w", repo.URL.Redacted(), ref.Name, err)
	}
	if err := worktree.materialize(ctx); err != nil {
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
func (w *Worktree) materialize(ctx context.Context) (err error) {
	ctx, unlock, err := w.paths.lockWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := w.paths.ensure(ctx, w.repo); err != nil {
		return err
	}
	if err := w.paths.prepareRef(ctx, prepareOptions{Repo: w.repo, Ref: w.Ref}); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err == nil {
		return w.reset(ctx)
	}
	if _, err := run(ctx, command{dir: w.paths.git, args: []string{"worktree", "prune"}}); err != nil {
		return err
	}
	_, err = run(ctx, command{dir: w.paths.git, args: []string{"worktree", "add", "--quiet", "--force", "--detach",
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
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
	return cachedRead(ctx, c.Root, repo, "files", sha, func(ctx context.Context) ([]string, error) {
		return c.files(ctx, repo, sha)
	})
}

func (c *Cache) files(ctx context.Context, repo *Repository, sha string) ([]string, error) {
	if !fullSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("list files: a full 40-character commit SHA is required")
	}
	paths := []string{}
	err := c.readRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, sha}}}, func(mirror string, _ []string) error {
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

type lockMode bool

const (
	exclusiveLock lockMode = false
	sharedLock    lockMode = true
)

// acquire takes a file lock in mode, waiting until ctx is done.
func acquire(ctx context.Context, path, what string, mode lockMode) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create %s directory: %w", what, err)
	}
	lock := flock.New(path)
	try := lock.TryLockContext
	if mode == sharedLock {
		try = lock.TryRLockContext
	}
	locked, err := try(ctx, lockRetry)
	if err != nil || !locked {
		return nil, fmt.Errorf("acquire %s %s: %w", what, path, errors.Join(err, ctx.Err()))
	}
	return lock, nil
}
