package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var (
	ErrSharedCacheUnsupported = errors.New("git cache mirrors belong to their repositories")
	ErrCacheBusy              = errors.New("git cache entry is in use")
	ErrCacheEntry             = errors.New("invalid git cache entry for this action")
)

// EvictOptions selects a repository mirror by cache key.
type EvictOptions struct{ Owner string }

type EvictResult struct {
	Owner      string   `json:"owner"`
	Dependents []string `json:"dependents,omitempty"`
}

// Evict removes one repository's mirror and idle worktrees. Pool maintenance
// reclaims the removed mirror's retention refs after reconciling other borrowers.
func (c *Cache) Evict(ctx context.Context, o EvictOptions) (_ EvictResult, err error) {
	paths, err := c.requireMirror(o.Owner)
	if err != nil {
		return EvictResult{}, err
	}
	_, unlock, err := paths.lockWrite(ctx)
	if err != nil {
		return EvictResult{}, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	leases, err := lockIdleWorktrees(filepath.Join(paths.dir, "worktrees"))
	if err != nil {
		return EvictResult{}, err
	}
	defer func() { err = errors.Join(err, unlockWorktrees(leases)) }()
	absolute, err := filepath.Abs(paths.git)
	if err != nil {
		return EvictResult{}, err
	}
	for _, lease := range leases {
		if err := os.RemoveAll(strings.TrimSuffix(lease.Path(), ".lock")); err != nil {
			return EvictResult{}, err
		}
	}
	for _, dir := range []string{paths.git, paths.pins} {
		if err := os.RemoveAll(dir); err != nil {
			return EvictResult{}, err
		}
	}
	pinnedCommits.Delete(absolute)
	return EvictResult{Owner: o.Owner}, removeIfExists(paths.metadataPath())
}

// SharedOwner describes the former owner-selection response for clients that
// still query the cache-owners endpoint during the transition.
type SharedOwner struct {
	Owner     string    `json:"owner"`
	Base      string    `json:"base"`
	Commits   int       `json:"commits"`
	Committed time.Time `json:"committed"`
}

func (c *Cache) SharedOwners(ctx context.Context, _ *Repository) ([]SharedOwner, error) {
	return []SharedOwner{}, ctx.Err()
}

type RebindOptions struct{ Owner string }

type RebindResult struct {
	Key      string `json:"key"`
	Previous string `json:"previous"`
	Owner    string `json:"owner"`
}

func (c *Cache) Rebind(context.Context, *Repository, RebindOptions) (RebindResult, error) {
	return RebindResult{}, ErrSharedCacheUnsupported
}

func validCacheKey(key string) bool {
	return key != "" && key != "." && key == filepath.Base(key) && filepath.IsLocal(key)
}

func (c *Cache) requireMirror(key string) (mirrorPaths, error) {
	if c.Root == "" || !validCacheKey(key) {
		return mirrorPaths{}, fmt.Errorf("%w: invalid cache root or key %q", ErrCacheEntry, key)
	}
	paths := newMirrorPaths(filepath.Join(c.cacheRoot(), key))
	root, err := os.OpenRoot(c.cacheRoot())
	if err != nil {
		return paths, fmt.Errorf("%w: %w", ErrCacheEntry, err)
	}
	defer root.Close()
	if _, err := root.Stat(filepath.Join(key, "mirror.git", "HEAD")); err != nil {
		return paths, fmt.Errorf("%w: mirror %s: %w", ErrCacheEntry, key, err)
	}
	meta, err := paths.metadata()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return paths, err
	}
	return c.withPool(paths, meta.Pool)
}

func lockIdleWorktrees(dir string) ([]*flock.Flock, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".lock") {
			names[strings.TrimSuffix(entry.Name(), ".lock")] = true
		}
	}
	var leases []*flock.Flock
	for name := range names {
		lock := flock.New(filepath.Join(dir, name+".lock"))
		locked, err := lock.TryLock()
		if err != nil || !locked {
			return nil, errors.Join(err, unlockWorktrees(leases), fmt.Errorf("%w: worktree %s is leased", ErrCacheBusy, name))
		}
		leases = append(leases, lock)
	}
	return leases, nil
}

func unlockWorktrees(leases []*flock.Flock) (err error) {
	for _, lock := range leases {
		err = errors.Join(err, lock.Unlock())
	}
	return err
}
