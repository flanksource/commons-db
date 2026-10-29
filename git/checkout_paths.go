package git

import (
	"context"
	"fmt"
	"path/filepath"
)

func (c *Cache) checkoutPaths(ctx context.Context, repo *Repository, ref Ref, depth int) (mirrorPaths, error) {
	if depth > 0 {
		if _, shared := repo.Provider.(RemoteHistoryProvider); !shared {
			return c.mirrorPaths(repo)
		}
		return newMirrorPaths(filepath.Join(c.Root, fmt.Sprintf("%s-depth-%d", repo.URL.CacheKey(), depth))), nil
	}
	if err := c.prepareShared(ctx, repo, ref); err != nil {
		return mirrorPaths{}, err
	}
	return c.mirrorPaths(repo)
}
