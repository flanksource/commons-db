package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	registry "github.com/flanksource/commons-db/git/internal/cache"
)

// prepareShared publishes a binding only after its pinned commit is present.
// The per-repository lock prevents two processes choosing different owners.
func (c *Cache) prepareShared(ctx context.Context, repo *Repository, ref Ref) (err error) {
	if c.Root == "" || repo == nil {
		return fmt.Errorf("git cache root and repository are required")
	}
	api, supported := repo.Provider.(RemoteHistoryProvider)
	if !supported {
		return nil
	}
	finish := startGitTiming(ctx, "cache_discovery")
	defer func() { finish(err, "", nil) }()
	key := repo.URL.CacheKey()
	lease, err := acquire(ctx, filepath.Join(c.Root, key, "repository.lock"), "repository discovery", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Unlock()) }()
	owner, err := registry.Load(c.Root, key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.Root, key, "repository.json")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.Root, key, "mirror.git", "HEAD")); errors.Is(err, os.ErrNotExist) {
		owner, err = c.findSharedOwner(ctx, repo, ref, api)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	paths := newMirrorPaths(filepath.Join(c.Root, owner))
	if err := c.populateShared(ctx, paths, repo, ref); err != nil {
		return err
	}
	if err := registry.Save(c.Root, key, owner); err != nil {
		return err
	}
	shared := int64(0)
	if owner != key {
		shared = 1
	}
	rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_cache", Counters: []rpchttp.TimingCounter{{Name: "shared", Value: shared}, {Name: "dedicated", Value: 1 - shared}}})
	return nil
}

func (c *Cache) findSharedOwner(ctx context.Context, repo *Repository, ref Ref, api RemoteHistoryProvider) (string, error) {
	entries, err := os.ReadDir(c.Root)
	if err != nil {
		return "", err
	}
	type candidate struct {
		name     string
		modified time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == repo.URL.CacheKey() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		candidates = append(candidates, candidate{entry.Name(), info.ModTime()})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	for _, entry := range candidates {
		paths := newMirrorPaths(filepath.Join(c.Root, entry.name))
		if _, err := os.Stat(filepath.Join(paths.git, "HEAD")); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		matched, err := sharedCandidate(ctx, sharedCandidateOptions{Paths: paths, Repository: repo, Ref: ref, API: api})
		if err != nil {
			return "", err
		}
		if matched {
			return entry.name, nil
		}
	}
	return repo.URL.CacheKey(), nil
}

type sharedCandidateOptions struct {
	Paths      mirrorPaths
	Repository *Repository
	Ref        Ref
	API        RemoteHistoryProvider
}

func sharedCandidate(ctx context.Context, o sharedCandidateOptions) (matched bool, err error) {
	lock, err := acquire(ctx, o.Paths.lock, "candidate mirror", sharedLock)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	shallow, err := run(ctx, command{dir: o.Paths.git, args: []string{"rev-parse", "--is-shallow-repository"}})
	if err != nil {
		return false, err
	}
	if shallow == "true" {
		return false, nil
	}
	tips, err := run(ctx, command{dir: o.Paths.git, args: []string{"for-each-ref", "--count=1", "--sort=-committerdate", "--format=%(objectname)"}})
	if err != nil || tips == "" {
		return false, err
	}
	roots, err := run(ctx, command{dir: o.Paths.git, args: []string{"rev-list", "--all", "--max-parents=0"}})
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	for _, sha := range strings.Fields(tips + "\n" + roots) {
		if seen[sha] {
			continue
		}
		seen[sha] = true
		finish := startGitTiming(ctx, "cache_probe")
		base, err := o.API.RemoteMergeBase(ctx, RemoteMergeBaseOptions{Repository: o.Repository.URL, BaseSHA: sha, HeadSHA: o.Ref.SHA})
		if errors.Is(err, ErrRevisionNotFound) || errors.Is(err, ErrUnrelatedHistories) {
			finish(nil, "", nil)
		} else {
			finish(err, "", nil)
		}
		if errors.Is(err, ErrRevisionNotFound) || errors.Is(err, ErrUnrelatedHistories) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("probe cached history for %s: %w", o.Repository.URL.Redacted(), err)
		}
		if !fullSHAPattern.MatchString(base) {
			return false, fmt.Errorf("remote returned invalid merge base %q", base)
		}
		if !hasCommit(ctx, o.Paths.git, base) {
			return false, fmt.Errorf("remote merge base %s is absent from candidate %s", base, o.Paths.git)
		}
		return true, nil
	}
	return false, nil
}

func (c *Cache) populateShared(ctx context.Context, paths mirrorPaths, repo *Repository, ref Ref) (err error) {
	lock, err := acquire(ctx, paths.lock, "shared mirror", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, paths.git); err != nil {
		return err
	}
	if !hasCommit(ctx, paths.git, ref.SHA) {
		pinned := Ref{Name: ref.SHA, FullName: ref.SHA, SHA: ref.SHA, Kind: RefSHA}
		sha, err := fetch(ctx, fetchOptions{Dir: paths.git, Owner: repo, Repo: repo, Ref: pinned})
		if err != nil {
			return err
		}
		if sha != ref.SHA {
			return fmt.Errorf("fetch returned %s, expected pinned commit %s", sha, ref.SHA)
		}
	}
	if err := recordCacheRef(ctx, paths.git, repo, ref); err != nil {
		return err
	}
	return paths.pin(ctx, []string{ref.SHA})
}

func (c *Cache) cloneCached(ctx context.Context, repo *Repository, ref Ref, options CloneOptions) (err error) {
	paths, err := c.cloneSource(ctx, repo, ref, options.Depth)
	if err != nil {
		return err
	}
	if options.Depth == 0 {
		if _, _, err := c.prepareReadRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, ref.SHA}}, History: true}); err != nil {
			return err
		}
	}
	mode := sharedLock
	if ref.Kind == RefTag {
		mode = exclusiveLock
	}
	lock, err := acquire(ctx, paths.lock, "clone source", mode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	sourceRef := ref.SHA
	if ref.Kind == RefTag {
		sourceRef, err = cachedCloneTag(ctx, cachedTagOptions{Paths: paths, Repository: repo, Ref: ref, Depth: options.Depth})
		if err != nil {
			return err
		}
	}
	return readRepository(ctx, paths.git, func() error {
		args := []string{"fetch", "--quiet", "--no-tags"}
		if options.Depth > 0 {
			args = append(args, fmt.Sprintf("--depth=%d", options.Depth))
		}
		absolute, err := filepath.Abs(paths.git)
		if err != nil {
			return err
		}
		// A local transport copies objects without creating a persistent alternate.
		source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
		spec := sourceRef
		if ref.Kind == RefTag {
			spec = "+" + sourceRef + ":refs/tags/" + ref.Name
		}
		if ref.Kind == RefBranch {
			spec = "+" + sourceRef + ":refs/remotes/origin/" + ref.Name
		}
		_, err = run(ctx, command{dir: options.Dir, args: append(args, "--end-of-options", source, spec)})
		return err
	})
}
