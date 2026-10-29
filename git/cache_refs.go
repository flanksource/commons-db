package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

func recordCacheRef(ctx context.Context, mirror string, repo *Repository, ref Ref) error {
	if _, ok := repo.Provider.(RemoteHistoryProvider); !ok {
		return nil
	}
	kind := "commits"
	name := ref.SHA
	switch ref.Kind {
	case RefBranch:
		kind, name = "heads", ref.Name
	case RefTag:
		return nil
	}
	_, err := run(ctx, command{dir: mirror, args: []string{"update-ref", "refs/commons-cache/repos/" + repo.URL.CacheKey() + "/" + kind + "/" + name, ref.SHA}})
	return err
}

func (c *Cache) cloneSource(ctx context.Context, repo *Repository, ref Ref, depth int) (result mirrorPaths, err error) {
	if c.Root == "" {
		return mirrorPaths{}, fmt.Errorf("git cache root is required")
	}
	if depth == 0 {
		return c.checkoutPaths(ctx, repo, ref, 0)
	}
	paths := newMirrorPaths(filepath.Join(c.Root, fmt.Sprintf("%s-depth-%d", repo.URL.CacheKey(), depth)))
	lock, err := acquire(ctx, paths.lock, "shallow clone source", exclusiveLock)
	if err != nil {
		return mirrorPaths{}, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, paths.git); err != nil {
		return mirrorPaths{}, err
	}
	if !hasCommit(ctx, paths.git, ref.SHA) {
		sha, err := fetch(ctx, fetchOptions{Dir: paths.git, Owner: repo, Repo: repo, Ref: Ref{Name: ref.SHA, FullName: ref.SHA, SHA: ref.SHA, Kind: RefSHA}, Depth: depth})
		if err != nil {
			return mirrorPaths{}, err
		}
		if sha != ref.SHA {
			return mirrorPaths{}, fmt.Errorf("shallow fetch returned %s, expected %s", sha, ref.SHA)
		}
	}
	return paths, paths.pin(ctx, []string{ref.SHA})
}
