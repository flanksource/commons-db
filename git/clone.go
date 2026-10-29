package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flanksource/commons/logger"
)

// CloneOptions configures Clone. An empty Ref is the remote HEAD; Depth 0
// fetches full history.
type CloneOptions struct {
	// Cache supplies an optional object cache; the clone remains independent.
	Cache *Cache
	Dir   string
	Ref   string
	Depth int
}

// Clone puts ref of repo into Dir: init (or reuse an existing checkout),
// fetch exactly that ref, and check it out (on a local branch of the same
// name for branches, detached otherwise). It returns the ref with SHA set to
// the commit checked out.
func Clone(ctx context.Context, repo *Repository, options CloneOptions) (Ref, error) {
	if options.Dir == "" {
		return Ref{}, errors.New("clone directory is required")
	}
	if options.Depth < 0 {
		return Ref{}, fmt.Errorf("invalid clone depth %d", options.Depth)
	}
	if options.Cache != nil && options.Cache.Root == "" {
		return Ref{}, errors.New("git cache root is required")
	}
	ref, err := repo.Resolve(ctx, options.Ref)
	if err != nil {
		return Ref{}, err
	}
	if err := prepareCloneDir(ctx, options.Dir, repo); err != nil {
		return Ref{}, err
	}
	if options.Cache != nil {
		err = options.Cache.cloneCached(ctx, repo, ref, options)
	} else {
		ref.SHA, err = fetch(ctx, fetchOptions{Dir: options.Dir, Owner: repo, Repo: repo, Ref: ref, Depth: options.Depth})
	}
	if err != nil {
		return Ref{}, err
	}
	if err := checkoutFetched(ctx, options.Dir, ref); err != nil {
		return Ref{}, err
	}
	logger.GetLogger("git").V(1).Infof("checked out %s@%s (%s) dir=%s", repo.URL.Redacted(), ref.Name, ref.SHA[:8], options.Dir)
	return ref, nil
}

// prepareCloneDir initializes Dir with an origin remote that carries no
// credentials, or accepts an existing repository; any other non-empty
// directory is refused.
func prepareCloneDir(ctx context.Context, dir string, repo *Repository) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read clone directory %s: %w", dir, err)
	case len(entries) > 0:
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
			return fmt.Errorf("clone directory %s is not empty and not a git repository", dir)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create clone directory %s: %w", dir, err)
	}
	if _, err := run(ctx, command{dir: dir, args: []string{"init", "--quiet"}}); err != nil {
		return err
	}
	_, err = run(ctx, command{dir: dir, args: []string{"remote", "add", "origin", "--end-of-options", repo.URL.Clone}})
	return err
}

func checkoutFetched(ctx context.Context, dir string, ref Ref) error {
	args := []string{"checkout", "--quiet", "--force", "--detach", "--end-of-options", ref.SHA}
	if ref.Kind == RefBranch {
		if err := ValidateBranchName(ref.Name); err != nil {
			return err
		}
		args = []string{"checkout", "--quiet", "--force", "-B", ref.Name, "--end-of-options", ref.SHA}
	}
	_, err := run(ctx, command{dir: dir, args: args})
	return err
}
