package git

import "context"

// ApplyInWorktree runs request through the worktree path Apply keeps for the
// strategies that need one, so a spec can compare it with the plumbing path.
func ApplyInWorktree(ctx context.Context, cache *Cache, repo *Repository, request ChangeRequest) (ChangeResult, error) {
	base, err := cache.preflight(ctx, repo, request)
	if err != nil {
		return ChangeResult{}, err
	}
	return cache.applyInWorktree(ctx, repo, base, request)
}
