package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func recordCacheRef(ctx context.Context, mirror string, repo *Repository, ref Ref) error {
	if ref.Kind != RefBranch {
		return nil
	}
	_, err := run(ctx, command{dir: mirror, args: []string{"update-ref", cacheRefPrefix + repo.URL.CacheKey() + "/heads/" + ref.Name, ref.SHA}})
	return err
}

// Endpoint reads can use an isolated shallow mirror if its snapshots are
// already present. History reads always prepare the complete mirror.
func (m mirrorPaths) lockShallowRefs(ctx context.Context, shas []string) (string, func() error, error) {
	if _, err := os.Stat(filepath.Join(m.git, "HEAD")); err == nil {
		return "", nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	parent := filepath.Dir(m.dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), filepath.Base(m.dir)+"-depth-") {
			continue
		}
		candidate := newMirrorPaths(filepath.Join(parent, entry.Name()))
		lock, err := acquire(ctx, candidate.lock, "shallow mirror lock", sharedLock)
		if err != nil {
			return "", nil, err
		}
		if !slices.ContainsFunc(shas, func(sha string) bool { return !hasCommit(ctx, candidate.git, sha) }) {
			return candidate.git, lock.Unlock, nil
		}
		if err := lock.Unlock(); err != nil {
			return "", nil, err
		}
	}
	return "", nil, ctx.Err()
}

func (c *Cache) checkoutPaths(repo *Repository, depth int) (mirrorPaths, error) {
	if depth < 0 {
		return mirrorPaths{}, fmt.Errorf("invalid checkout depth %d", depth)
	}
	paths, err := c.mirrorPaths(repo)
	if err != nil || depth == 0 {
		return paths, err
	}
	return newMirrorPaths(filepath.Join(c.cacheRoot(), fmt.Sprintf("%s-depth-%d", repo.URL.CacheKey(), depth))), nil
}

func (m mirrorPaths) prepareRef(ctx context.Context, store Store, options fetchOptions) error {
	ref := options.Ref
	if pinned, err := m.usePinned([]string{ref.SHA}, options.Depth == 0); err != nil {
		return err
	} else if pinned {
		return recordCacheRef(ctx, m.git, options.Repo, ref)
	}
	if options.Depth == 0 {
		if err := hydrateMiss(ctx, store, m.git, ref.SHA); err != nil {
			return err
		}
	}
	options.Ref = Ref{Name: ref.Name, FullName: ref.SHA, SHA: ref.SHA, Kind: RefSHA}
	options.Pool = m.poolName()
	var err error
	sha := ref.SHA
	if options.Depth == 0 {
		sha, err = fetchHistory(ctx, options)
	} else if !hasCommit(ctx, m.git, ref.SHA) {
		sha, err = fetch(ctx, options)
	}
	if err != nil {
		return err
	}
	if sha != ref.SHA {
		return fmt.Errorf("fetch returned %s, expected %s", sha, ref.SHA)
	}
	if err := m.retain(ctx, []string{sha}, true); err != nil {
		return err
	}
	if err := recordCacheRef(ctx, m.git, options.Repo, ref); err != nil {
		return err
	}
	return m.pin(ctx, []string{sha})
}
