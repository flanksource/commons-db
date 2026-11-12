package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	lease, err := acquire(ctx, c.repositoryLock(key), "repository discovery", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Unlock()) }()
	binding, err := registry.Load(c.Root, key)
	if errors.Is(err, registry.ErrOwnerMissing) {
		err = registry.Remove(c.Root, key)
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.Root, key, "repository.json")); err == nil {
		if binding.Repository != "" {
			return nil
		}
		binding.Repository = repo.URL.Redacted()
		return registry.Save(c.Root, key, binding)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	owner := key
	if _, err := os.Stat(filepath.Join(c.Root, key, "mirror.git", "HEAD")); errors.Is(err, os.ErrNotExist) {
		if owner, err = c.findSharedOwner(ctx, repo, ref, api); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return c.bindShared(ctx, repo, ref, owner)
}

func (c *Cache) repositoryLock(key string) string {
	return filepath.Join(c.Root, key, "repository.lock")
}

// bindShared makes ref's commit present in owner's mirror, then publishes
// repo's binding to owner. The caller holds repo's repository lock.
func (c *Cache) bindShared(ctx context.Context, repo *Repository, ref Ref, owner string) error {
	if err := c.populateShared(ctx, newMirrorPaths(filepath.Join(c.Root, owner)), repo, ref); err != nil {
		return err
	}
	if err := registry.Save(c.Root, repo.URL.CacheKey(), registry.Binding{Owner: owner, Repository: repo.URL.Redacted()}); err != nil {
		return err
	}
	shared := int64(0)
	if owner != repo.URL.CacheKey() {
		shared = 1
	}
	rpchttp.AddTiming(ctx, rpchttp.TimingMetric{Name: "git_cache", Counters: []rpchttp.TimingCounter{{Name: "shared", Value: shared}, {Name: "dedicated", Value: 1 - shared}}})
	return nil
}

// findSharedOwner is the mirror sharing the most history with ref, or repo's
// own cache key when no cached mirror shares any.
func (c *Cache) findSharedOwner(ctx context.Context, repo *Repository, ref Ref, api RemoteHistoryProvider) (string, error) {
	owners, err := c.sharedOwners(ctx, repo, ref, api)
	if err != nil || len(owners) == 0 {
		return repo.URL.CacheKey(), err
	}
	return owners[0].Owner, nil
}

// SharedOwner is a cached mirror holding part of a repository's history: Base
// is the merge base of the probed ref with the mirror's newest commit, Commits
// the number of commits in Base's history and Committed when Base was
// committed.
type SharedOwner struct {
	Owner     string    `json:"owner"`
	Base      string    `json:"base"`
	Commits   int       `json:"commits"`
	Committed time.Time `json:"committed"`
}

// SharedOwners ranks the cached mirrors, other than repo's own, that share
// history with repo's remote HEAD: the most shared commits (the fewest left to
// fetch) first, then the most recently modified mirror. Rebind without an
// Owner binds repo to the first.
func (c *Cache) SharedOwners(ctx context.Context, repo *Repository) ([]SharedOwner, error) {
	api, ref, err := c.sharedProbe(ctx, repo)
	if err != nil {
		return nil, err
	}
	return c.sharedOwners(ctx, repo, ref, api)
}

// sharedProbe is repo's remote history API and the commit (its remote HEAD)
// mirrors are compared against when choosing an owner outside a read.
func (c *Cache) sharedProbe(ctx context.Context, repo *Repository) (RemoteHistoryProvider, Ref, error) {
	if c.Root == "" || repo == nil {
		return nil, Ref{}, fmt.Errorf("git cache root and repository are required")
	}
	api, supported := repo.Provider.(RemoteHistoryProvider)
	if !supported {
		return nil, Ref{}, fmt.Errorf("%w: %s cannot compare remote history, so it never shares a mirror", ErrSharedCacheUnsupported, repo.URL.Redacted())
	}
	head, err := repo.Resolve(ctx, "")
	if err != nil {
		return nil, Ref{}, err
	}
	return api, Ref{Name: head.SHA, FullName: head.SHA, SHA: head.SHA, Kind: RefSHA}, nil
}

func (c *Cache) sharedOwners(ctx context.Context, repo *Repository, ref Ref, api RemoteHistoryProvider) ([]SharedOwner, error) {
	entries, err := os.ReadDir(c.Root)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		candidates = append(candidates, candidate{entry.Name(), info.ModTime()})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	var owners []SharedOwner
	for _, entry := range candidates {
		paths := newMirrorPaths(filepath.Join(c.Root, entry.name))
		if _, err := os.Stat(filepath.Join(paths.git, "HEAD")); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		owner, matched, err := sharedCandidate(ctx, sharedCandidateOptions{Paths: paths, Repository: repo, Ref: ref, API: api})
		if err != nil {
			return nil, err
		}
		if matched {
			owner.Owner = entry.name
			owners = append(owners, owner)
		}
	}
	sort.SliceStable(owners, func(i, j int) bool { return owners[i].Commits > owners[j].Commits })
	return owners, nil
}

type sharedCandidateOptions struct {
	Paths      mirrorPaths
	Repository *Repository
	Ref        Ref
	API        RemoteHistoryProvider
}

// sharedCandidate probes whether the mirror at o.Paths shares history with
// o.Ref, through its newest commit first and then its root commits.
func sharedCandidate(ctx context.Context, o sharedCandidateOptions) (owner SharedOwner, matched bool, err error) {
	lock, err := acquire(ctx, o.Paths.lock, "candidate mirror", sharedLock)
	if err != nil {
		return owner, false, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	shallow, err := run(ctx, command{dir: o.Paths.git, args: []string{"rev-parse", "--is-shallow-repository"}})
	if err != nil {
		return owner, false, err
	}
	if shallow == "true" {
		return owner, false, nil
	}
	tips, err := run(ctx, command{dir: o.Paths.git, args: []string{"for-each-ref", "--count=1", "--sort=-committerdate", "--format=%(objectname)"}})
	if err != nil || tips == "" {
		return owner, false, err
	}
	roots, err := run(ctx, command{dir: o.Paths.git, args: []string{"rev-list", "--all", "--max-parents=0"}})
	if err != nil {
		return owner, false, err
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
			return owner, false, fmt.Errorf("probe cached history for %s: %w", o.Repository.URL.Redacted(), err)
		}
		if !fullSHAPattern.MatchString(base) {
			return owner, false, fmt.Errorf("remote returned invalid merge base %q", base)
		}
		if !hasCommit(ctx, o.Paths.git, base) {
			return owner, false, fmt.Errorf("remote merge base %s is absent from candidate %s", base, o.Paths.git)
		}
		owner, err = describeBase(ctx, o.Paths.git, base)
		return owner, err == nil, err
	}
	return owner, false, nil
}

// describeBase counts the history of base in mirror and reads its commit time.
func describeBase(ctx context.Context, mirror, base string) (SharedOwner, error) {
	count, err := run(ctx, command{dir: mirror, args: []string{"rev-list", "--count", "--end-of-options", base}})
	if err != nil {
		return SharedOwner{}, err
	}
	commits, err := strconv.Atoi(count)
	if err != nil {
		return SharedOwner{}, fmt.Errorf("count history of %s in %s: unexpected %q", base, mirror, count)
	}
	seconds, err := run(ctx, command{dir: mirror, args: []string{"show", "-s", "--format=%ct", "--end-of-options", base}})
	if err != nil {
		return SharedOwner{}, err
	}
	unix, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return SharedOwner{}, fmt.Errorf("commit time of %s in %s: unexpected %q", base, mirror, seconds)
	}
	return SharedOwner{Base: base, Commits: commits, Committed: time.Unix(unix, 0).UTC()}, nil
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
