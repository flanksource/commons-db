package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// FetchOptions configures Cache.Fetch. Refs are branches, tags and full
// commit SHAs (e.g. pull request heads), fetched in one git fetch. Publish
// then writes the mirror's new objects and refs to Cache.Store.
type FetchOptions struct {
	Refs    []Ref
	Publish bool
}

// Fetch brings refs into repo's mirror ahead of any read. It uses cached
// SHAs from the mirror and its pool, then hydrates missing commits from the
// Store. One remote fetch advances named refs and fills remaining misses.
// The resulting commits are pinned so they survive gc and reach the Store.
func (c *Cache) Fetch(ctx context.Context, repo *Repository, options FetchOptions) (err error) {
	if len(options.Refs) == 0 {
		return errors.New("git fetch: at least one ref is required")
	}
	if options.Publish && c.Store == nil {
		return fmt.Errorf("git fetch %s: publish requested but the cache has no store", repo.URL.Redacted())
	}
	paths, err := c.mirrorPaths(repo)
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
	shas, err := fetchRefs(ctx, paths, c.Store, repo, options.Refs)
	if err != nil {
		return err
	}
	if err := paths.retain(ctx, shas, true); err != nil {
		return err
	}
	if len(shas) > 0 {
		if err := paths.pin(ctx, shas); err != nil {
			return err
		}
	}
	if !options.Publish {
		return nil
	}
	return publishMirror(ctx, c.Store, paths.git)
}

// fetchRefs batches remote refs and returns the commits they resolved to.
func fetchRefs(ctx context.Context, paths mirrorPaths, store Store, repo *Repository, refs []Ref) ([]string, error) {
	var refspecs, shas, names []string
	var destinations []string
	for _, ref := range refs {
		names = append(names, ref.Name)
		switch ref.Kind {
		case RefBranch, RefTag, RefHead:
			spec := ref.refspec(originRemote)
			if ref.Kind == RefHead {
				spec = "+HEAD:refs/commons-cache/remote-head"
			}
			_, destination, _ := strings.Cut(spec, ":")
			refspecs = append(refspecs, spec)
			destinations = append(destinations, destination+"^{commit}")
			continue
		case RefSHA:
		default:
			return nil, fmt.Errorf("git fetch %s: unknown ref kind %q of %q", repo.URL.Redacted(), ref.Kind, ref.Name)
		}
		if !fullSHAPattern.MatchString(ref.SHA) {
			return nil, fmt.Errorf("git fetch %s: %q is not a full 40-character commit SHA", repo.URL.Redacted(), ref.SHA)
		}
		shas = append(shas, ref.SHA)
	}
	if len(destinations) > 0 {
		if _, err := hydrate(ctx, store, paths.git); err != nil {
			return nil, err
		}
	} else if err := hydrateMiss(ctx, store, paths.git, shas...); err != nil {
		return nil, err
	}
	for _, sha := range shas {
		if !hasCommit(ctx, paths.git, sha) {
			refspecs = append(refspecs, sha)
		}
	}
	if len(refspecs) == 0 {
		return shas, nil
	}
	described := Ref{Name: strings.Join(names, ", ")}
	if _, err := fetch(ctx, fetchOptions{Dir: paths.git, Owner: repo, Repo: repo, Ref: described, Refspecs: refspecs, Flags: []string{"--atomic"}, Pool: paths.poolName()}); err != nil {
		return nil, err
	}
	if len(destinations) > 0 {
		resolved, err := run(ctx, command{dir: paths.git, args: append([]string{"rev-parse", "--revs-only", "--end-of-options"}, destinations...)})
		if err != nil {
			return nil, err
		}
		shas = append(shas, lines(resolved)...)
	}
	return slices.Compact(slices.Sorted(slices.Values(shas))), nil
}
