package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons/logger"
)

func (c *Cache) maintainPools(ctx context.Context) error {
	dir := filepath.Join(c.cacheRoot(), "pools")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := c.maintainPool(ctx, filepath.Join(dir, entry.Name(), "pool.git")); err != nil {
				return fmt.Errorf("maintain git object pool %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func (c *Cache) maintainPool(ctx context.Context, pool string) (err error) {
	paths := mirrorPaths{pool: pool}
	lock, err := acquire(ctx, paths.poolLock(), "object pool", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if _, err := os.Stat(filepath.Join(pool, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	keys, err := c.cacheKeys()
	if err != nil {
		return err
	}
	borrowers := map[string]bool{}
	for _, key := range keys {
		mirror := newMirrorPaths(filepath.Join(c.cacheRoot(), key))
		meta, err := mirror.metadata()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if meta.Pool == paths.poolName() {
			borrowers[key] = true
			mirror.pool = pool
			if err := reconcilePoolBorrower(ctx, mirror); err != nil {
				return err
			}
		}
	}
	if err := prunePoolRefs(ctx, pool, borrowers); err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: pool, args: []string{"maintenance", "run", "--task=gc", "--quiet"}}); err != nil {
		return err
	}
	logger.GetLogger("git").Infof("maintained git object pool %s for %d mirrors", paths.poolName(), len(borrowers))
	return nil
}

// A process may stop after Git updates a fetched ref and before recording its
// retention ref. Reconcile complete mirror roots before pruning any donor.
func reconcilePoolBorrower(ctx context.Context, mirror mirrorPaths) (err error) {
	lock, err := acquire(ctx, mirror.lock, "reconcile pool borrower", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	out, err := run(ctx, command{dir: mirror.git, args: []string{"for-each-ref", "--format=%(objecttype) %(objectname) %(*objecttype) %(*objectname)",
		"refs/remotes/origin", "refs/tags", "refs/commons-cache/remote-head", cacheRefPrefix, pinRefPrefix, storeRefPrefix}})
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

func prunePoolRefs(ctx context.Context, pool string, borrowers map[string]bool) error {
	out, err := run(ctx, command{dir: pool, args: []string{"for-each-ref", "--format=%(refname)", poolTips, poolRetained}})
	if err != nil {
		return err
	}
	var updates strings.Builder
	for _, ref := range lines(out) {
		_, path, found := strings.Cut(strings.TrimPrefix(ref, "refs/commons-pool/"), "/")
		key, _, hasSHA := strings.Cut(path, "/")
		if !found || !hasSHA {
			return fmt.Errorf("invalid pool ref %q", ref)
		}
		if !borrowers[key] {
			fmt.Fprintf(&updates, "delete %s\n", ref)
		}
	}
	if updates.Len() == 0 {
		return nil
	}
	_, err = run(ctx, command{dir: pool, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())})
	return err
}
