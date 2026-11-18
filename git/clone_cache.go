package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
)

func (c *Cache) cloneCached(ctx context.Context, repo *Repository, ref Ref, options CloneOptions) (err error) {
	paths, err := c.checkoutPaths(repo, options.Depth)
	if err != nil {
		return err
	}
	unlock, err := paths.lockWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := paths.ensure(ctx, repo); err != nil {
		return err
	}
	if err := paths.prepareRef(ctx, c.Store, fetchOptions{Dir: paths.git, Owner: repo, Repo: repo, Ref: ref, Depth: options.Depth}); err != nil {
		return err
	}
	spec := ref.SHA
	switch ref.Kind {
	case RefTag:
		tag, err := cachedCloneTag(ctx, cachedTagOptions{Paths: paths, Repository: repo, Ref: ref, Depth: options.Depth})
		if err != nil {
			return err
		}
		spec = "+" + tag + ":refs/tags/" + ref.Name
	case RefBranch:
		spec = "+" + ref.SHA + ":refs/remotes/origin/" + ref.Name
	}
	args := []string{"fetch", "--quiet", "--no-tags"}
	if options.Depth > 0 {
		args = append(args, fmt.Sprintf("--depth=%d", options.Depth))
	}
	absolute, err := filepath.Abs(paths.git)
	if err != nil {
		return err
	}
	source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
	_, err = run(ctx, command{dir: options.Dir, args: append(args, "--end-of-options", source, spec)})
	return err
}
