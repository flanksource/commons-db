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

	"github.com/flanksource/commons/logger"
)

func (c *Cache) maintainPools(ctx context.Context) error {
	entries, err := os.ReadDir(c.poolsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := c.maintainPool(ctx, entry.Name()); err != nil {
				return fmt.Errorf("maintain git object pool %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// maintainPool reconciles this cache's borrowers, prunes the repositories no
// cache used for PoolRetain and collects the pool, under its exclusive lease.
// A lost lease stops it before pruning or collection.
func (c *Cache) maintainPool(ctx context.Context, name string) (err error) {
	paths, err := c.withPool(mirrorPaths{}, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(paths.pool, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	ctx, release, err := holdLease(ctx, "git object pool "+name+" maintenance lease", paths.locks.ReadWriteLock(paths.poolLease(""), paths.holder).Lock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	borrowers, err := c.reconcilePoolBorrowers(ctx, paths)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	pruned, err := prunePoolRefs(ctx, paths.pool, time.Now().Add(-c.poolRetain()))
	if err != nil {
		return err
	}
	if err := collectPool(ctx, paths.pool); err != nil {
		return err
	}
	logger.GetLogger("git").Infof("maintained git object pool %s for %d local mirrors, pruned %d repositories", name, borrowers, pruned)
	return nil
}

// collectPool runs gc with an explicit prune grace, so a pack another host is
// mid-read survives, then writes the commit-graph and multi-pack-index (with
// bitmaps) that history walks read instead of objects.
func collectPool(ctx context.Context, pool string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: pool, args: []string{"-c", "gc.pruneExpire=" + poolPruneExpire, "maintenance", "run",
		"--task=gc", "--task=commit-graph", "--task=incremental-repack", "--quiet"}}); err != nil {
		return err
	}
	_, err := run(ctx, command{dir: pool, args: []string{"multi-pack-index", "write", "--bitmap", "--no-progress"}})
	return err
}

func (c *Cache) reconcilePoolBorrowers(ctx context.Context, pool mirrorPaths) (int, error) {
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	keys, err := c.cacheKeys()
	if err != nil {
		return 0, err
	}
	borrowers := 0
	for _, key := range keys {
		mirror := newMirrorPaths(filepath.Join(c.cacheRoot(), key))
		meta, err := mirror.metadata()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return borrowers, err
		}
		if meta.Pool == pool.poolName() {
			borrowers++
			mirror.pool, mirror.locks = pool.pool, pool.locks
			if err := reconcilePoolBorrower(ctx, mirror); err != nil {
				return borrowers, err
			}
		}
	}
	return borrowers, nil
}

// A process may stop after Git updates a fetched ref and before recording its
// retention ref. Reconcile complete mirror roots before pruning any donor.
func reconcilePoolBorrower(ctx context.Context, mirror mirrorPaths) (err error) {
	lock, err := acquire(ctx, mirror.lock, "reconcile pool borrower", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := requireComplete(mirror.git); err != nil {
		return err
	}
	out, err := run(ctx, command{dir: mirror.git, args: []string{"for-each-ref", "--format=%(objecttype) %(objectname) %(*objecttype) %(*objectname)",
		"refs/remotes/origin", "refs/tags", "refs/commons-cache/remote-head", cacheRefPrefix, pinRefPrefix}})
	if err != nil {
		return err
	}
	var shas []string
	for _, line := range lines(out) {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 2 && fields[0] == "commit":
			shas = append(shas, fields[1])
		case len(fields) == 4 && fields[2] == "commit":
			shas = append(shas, fields[3])
		}
	}
	return mirror.retain(ctx, shas, false)
}

// prunePoolRefs deletes the refs and stamp of every repository last used
// before cutoff, whichever cache used it. A repository with refs and no
// stamp (a pool from before stamps) is stamped now and kept.
func prunePoolRefs(ctx context.Context, pool string, cutoff time.Time) (int, error) {
	refs, err := poolRefsByRepository(ctx, pool)
	if err != nil {
		return 0, err
	}
	var updates strings.Builder
	var stale []string
	for key, names := range refs {
		used, stamped, err := readStamp(pool, key)
		if err != nil {
			return 0, err
		}
		if !stamped {
			if err := writeStamp(pool, key, time.Now()); err != nil {
				return 0, err
			}
			continue
		}
		if !used.Before(cutoff) {
			continue
		}
		stale = append(stale, key)
		for _, ref := range names {
			fmt.Fprintf(&updates, "delete %s\n", ref)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	if _, err := run(ctx, command{dir: pool, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())}); err != nil {
		return 0, err
	}
	for _, key := range stale {
		if err := removeIfExists(filepath.Join(stampsDir(pool), key)); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}

// poolRefsByRepository groups the pool's tips and retained refs by the
// repository key that follows their namespace.
func poolRefsByRepository(ctx context.Context, pool string) (map[string][]string, error) {
	out, err := run(ctx, command{dir: pool, args: []string{"for-each-ref", "--format=%(refname)", poolTips, poolRetained}})
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	for _, ref := range lines(out) {
		_, path, found := strings.Cut(strings.TrimPrefix(ref, "refs/commons-pool/"), "/")
		key, _, hasSHA := strings.Cut(path, "/")
		if !found || !hasSHA || !validCacheKey(key) {
			return nil, fmt.Errorf("invalid pool ref %q", ref)
		}
		refs[key] = append(refs[key], ref)
	}
	return refs, nil
}
