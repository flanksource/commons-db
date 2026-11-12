package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	registry "github.com/flanksource/commons-db/git/internal/cache"
	"github.com/gofrs/flock"
)

var (
	// ErrSharedCacheUnsupported is returned when a repository's provider
	// cannot compare remote history, so its mirror is never shared.
	ErrSharedCacheUnsupported = errors.New("shared git cache is not supported for this repository")
	// ErrCacheBusy is returned when a cache action would remove a worktree a
	// Checkout has leased.
	ErrCacheBusy = errors.New("git cache entry is in use")
	// ErrCacheEntry is returned for a cache action on a key that does not
	// have the state the action needs (no mirror to evict, nothing to rebind).
	ErrCacheEntry = errors.New("invalid git cache entry for this action")
)

// EvictOptions configures Cache.Evict: Owner is the cache key of a mirror.
type EvictOptions struct {
	Owner string
}

// EvictResult names the evicted mirror and the repositories that were bound
// to it; each clones again, into the best remaining owner, on its next read.
type EvictResult struct {
	Owner      string   `json:"owner"`
	Dependents []string `json:"dependents,omitempty"`
}

// Evict deletes a mirror with its pins and worktrees, and the bindings and
// worktrees of the repositories bound to it. It waits for reads and fetches in
// flight, and fails with ErrCacheBusy while one of those worktrees is leased.
// A Tree opened before Evict fails on its next read.
func (c *Cache) Evict(ctx context.Context, options EvictOptions) (result EvictResult, err error) {
	owner := options.Owner
	if !validCacheKey(owner) {
		return result, fmt.Errorf("%w: invalid cache key %q", ErrCacheEntry, owner)
	}
	if err := c.requireMirror(owner); err != nil {
		return result, err
	}
	binding, err := registry.Load(c.Root, owner)
	if err != nil {
		return result, err
	}
	if binding.Owner != owner {
		return result, fmt.Errorf("%w: %s reads the mirror of %s; rebind it instead", ErrCacheEntry, owner, binding.Owner)
	}
	paths := newMirrorPaths(filepath.Join(c.Root, owner))
	lock, err := acquire(ctx, paths.lock, "mirror lock", exclusiveLock)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	result.Owner = owner
	if result.Dependents, err = c.dependentsOf(owner); err != nil {
		return result, err
	}
	keys := append([]string{owner}, result.Dependents...)
	leases, err := c.leaseWorktrees(keys)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, leases.release()) }()
	absolute, err := filepath.Abs(paths.git)
	if err != nil {
		return result, err
	}
	pinnedCommits.Delete(absolute)
	for _, dir := range []string{paths.git, paths.pins} {
		if err := os.RemoveAll(dir); err != nil {
			return result, fmt.Errorf("evict %s: %w", owner, err)
		}
	}
	for _, key := range keys {
		if err := leases.remove(key); err != nil {
			return result, err
		}
		if err := registry.Remove(c.Root, key); err != nil {
			return result, err
		}
	}
	return result, nil
}

// RebindOptions configures Cache.Rebind: Owner is the cache key of the mirror
// to bind to; empty binds to the best of SharedOwners (or a dedicated mirror
// when none shares history).
type RebindOptions struct {
	Owner string
}

// RebindResult is the repository's cache key with its previous and new owner.
type RebindResult struct {
	Key      string `json:"key"`
	Previous string `json:"previous"`
	Owner    string `json:"owner"`
}

// Rebind moves a repository bound to another repository's mirror onto a
// different mirror: it fetches the repository's remote HEAD into the new
// owner, publishes the binding, then removes the repository's worktrees and
// recorded refs from the previous owner. It fails with ErrCacheBusy while one
// of the repository's worktrees is leased, and with ErrUnrelatedHistories for
// an Owner sharing no history with it.
func (c *Cache) Rebind(ctx context.Context, repo *Repository, options RebindOptions) (result RebindResult, err error) {
	api, ref, err := c.sharedProbe(ctx, repo)
	if err != nil {
		return result, err
	}
	key := repo.URL.CacheKey()
	lease, err := acquire(ctx, c.repositoryLock(key), "repository rebind", exclusiveLock)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Unlock()) }()
	previous, err := c.sharedBinding(key)
	if err != nil {
		return result, err
	}
	result = RebindResult{Key: key, Previous: previous}
	owner := options.Owner
	switch {
	case owner == "":
		if result.Owner, err = c.findSharedOwner(ctx, repo, ref, api); err != nil {
			return result, err
		}
	case !validCacheKey(owner):
		return result, fmt.Errorf("%w: invalid cache key %q", ErrCacheEntry, owner)
	default:
		if err := c.requireRelated(ctx, owner, sharedCandidateOptions{Repository: repo, Ref: ref, API: api}); err != nil {
			return result, err
		}
		result.Owner = owner
	}
	if result.Owner == previous {
		return result, nil
	}
	leases, err := c.leaseWorktrees([]string{key})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, leases.release()) }()
	if err := c.bindShared(ctx, repo, ref, result.Owner); err != nil {
		return result, err
	}
	if err := leases.remove(key); err != nil {
		return result, err
	}
	return result, c.forgetRepository(ctx, key, previous)
}

// sharedBinding is the owner of key's binding to another repository's mirror;
// an evicted owner reads as "" so a rebind leaves nothing behind in it.
func (c *Cache) sharedBinding(key string) (string, error) {
	if _, err := os.Stat(filepath.Join(c.Root, key, "repository.json")); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s has no binding; it is bound on its first read", ErrCacheEntry, key)
	} else if err != nil {
		return "", err
	}
	binding, err := registry.Load(c.Root, key)
	if errors.Is(err, registry.ErrOwnerMissing) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if binding.Owner == key {
		return "", fmt.Errorf("%w: %s owns its mirror; evict it instead", ErrCacheEntry, key)
	}
	return binding.Owner, nil
}

// dependentsOf lists the keys bound to owner's mirror.
func (c *Cache) dependentsOf(owner string) ([]string, error) {
	keys, err := c.cacheKeys()
	if err != nil {
		return nil, err
	}
	var dependents []string
	for _, key := range keys {
		binding, err := registry.Load(c.Root, key)
		if err != nil {
			return nil, err
		}
		if binding.Owner == owner && key != owner {
			dependents = append(dependents, key)
		}
	}
	return dependents, nil
}

// validCacheKey reports whether key names one entry directly under the cache
// root, so a key from a request cannot address a path outside it.
func validCacheKey(key string) bool {
	return key != "" && key == filepath.Base(key) && filepath.IsLocal(key)
}

func (c *Cache) requireMirror(key string) error {
	if !validCacheKey(key) {
		return fmt.Errorf("%w: invalid cache key %q", ErrCacheEntry, key)
	}
	if _, err := os.Stat(filepath.Join(c.Root, key, "mirror.git", "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s has no mirror", ErrCacheEntry, key)
	} else if err != nil {
		return err
	}
	return nil
}

// requireRelated fails unless owner's mirror shares history with probe.Ref.
func (c *Cache) requireRelated(ctx context.Context, owner string, probe sharedCandidateOptions) error {
	if err := c.requireMirror(owner); err != nil {
		return err
	}
	probe.Paths = newMirrorPaths(filepath.Join(c.Root, owner))
	_, matched, err := sharedCandidate(ctx, probe)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("bind %s to %s: %w", probe.Repository.URL.Redacted(), owner, ErrUnrelatedHistories)
	}
	return nil
}

// forgetRepository deletes key's recorded refs and stale worktree records from
// the previous owner's mirror; its pins expire through Maintain.
func (c *Cache) forgetRepository(ctx context.Context, key, previous string) (err error) {
	if previous == "" {
		return nil
	}
	paths := newMirrorPaths(filepath.Join(c.Root, previous))
	lock, err := acquire(ctx, paths.lock, "mirror lock", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	refs, err := run(ctx, command{dir: paths.git, args: []string{"for-each-ref", "--format=%(refname)", cacheRefPrefix + key + "/"}})
	if err != nil {
		return err
	}
	if refs != "" {
		deletes := "delete " + strings.ReplaceAll(refs, "\n", "\ndelete ") + "\n"
		if _, err := run(ctx, command{dir: paths.git, args: []string{"update-ref", "--stdin"}, stdin: []byte(deletes)}); err != nil {
			return err
		}
	}
	_, err = run(ctx, command{dir: paths.git, args: []string{"worktree", "prune"}})
	return err
}

// worktreeLeases holds, per cache key, the lease of every worktree an action
// removes, so no Checkout starts on one meanwhile.
type worktreeLeases map[string][]*flock.Flock

// leaseWorktrees takes the lease of every worktree of keys without waiting,
// failing with ErrCacheBusy on the first one a Checkout holds.
func (c *Cache) leaseWorktrees(keys []string) (worktreeLeases, error) {
	leases := worktreeLeases{}
	for _, key := range keys {
		dir := filepath.Join(c.Root, key, "worktrees")
		states, err := worktreeStates(dir)
		if err != nil {
			return nil, errors.Join(err, leases.release())
		}
		for _, state := range states {
			lock := flock.New(filepath.Join(dir, state.Name+".lock"))
			locked, err := lock.TryLock()
			if err != nil || !locked {
				return nil, errors.Join(err, leases.release(), fmt.Errorf("%w: worktree %s of %s is leased", ErrCacheBusy, state.Name, key))
			}
			leases[key] = append(leases[key], lock)
		}
	}
	return leases, nil
}

// remove deletes key's worktrees directory, lease files included.
func (l worktreeLeases) remove(key string) error {
	for _, lock := range l[key] {
		if err := os.RemoveAll(strings.TrimSuffix(lock.Path(), ".lock")); err != nil {
			return fmt.Errorf("remove worktree %s: %w", lock.Path(), err)
		}
	}
	return nil
}

func (l worktreeLeases) release() error {
	var err error
	for _, locks := range l {
		for _, lock := range locks {
			err = errors.Join(err, lock.Unlock())
		}
	}
	return err
}
