package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flanksource/commons/logger"
	"github.com/gofrs/flock"
)

// legacyFiles are the entries a pre-v2 cache kept directly under
// <Root>/<key>: a mirror, its locks, pins, worktrees and the repository.json
// owner binding, each with the type it had. A directory holding anything else
// is not a legacy entry and is never touched.
var legacyFiles = map[string]bool{
	"mirror.git": true, "pins": true, "worktrees": true,
	"mirror.lock": false, "repository.lock": false, "repository.json": false,
}

// legacyBinding is a pre-v2 repository.json: the repository the directory
// caches and the key of the mirror that owned its objects.
type legacyBinding struct {
	Version    int    `json:"version"`
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
}

// migrateLegacy imports every pre-v2 entry's mirror into the v2 layout and
// removes the entry. An entry is legacy only when it matches that layout
// exactly (legacyEntry). An import needs no network: it fetches the legacy
// mirror's refs from disk into the repository's v2 pool (advertised there as
// tips, so later fetches negotiate against them) or, without a pool, into
// its v2 mirror. A failed import leaves the entry in place and is returned,
// after every other entry was tried.
func (c *Cache) migrateLegacy(ctx context.Context) error {
	entries, err := os.ReadDir(c.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list git cache %s: %w", c.Root, err)
	}
	var failed error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == cacheLayout {
			continue
		}
		dir := filepath.Join(c.Root, entry.Name())
		binding, legacy, err := legacyEntry(dir)
		if err == nil && legacy {
			err = c.migrateLegacyEntry(ctx, dir, binding)
		}
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("migrate legacy git cache %s: %w", dir, err))
		}
	}
	return failed
}

// legacyEntry reports whether dir is exactly a pre-v2 entry: only legacy
// files of their legacy types, and either a valid version 1 binding or no
// more than its empty lock files. Its binding is nil for a lock-only entry.
func legacyEntry(dir string) (*legacyBinding, bool, error) {
	if !validCacheKey(filepath.Base(dir)) {
		return nil, false, nil
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	names := map[string]bool{}
	for _, child := range children {
		isDir, known := legacyFiles[child.Name()]
		if !known || child.IsDir() != isDir || (!isDir && !child.Type().IsRegular()) {
			return nil, false, nil
		}
		names[child.Name()] = true
	}
	if !names["repository.json"] {
		lockOnly := len(names) > 0 && !names["mirror.git"] && !names["pins"] && !names["worktrees"]
		return nil, lockOnly && emptyFiles(dir, names), nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "repository.json"))
	if err != nil {
		return nil, false, err
	}
	var binding legacyBinding
	if err := json.Unmarshal(data, &binding); err != nil || binding.Version != 1 || !validCacheKey(binding.Owner) || binding.Repository == "" {
		return nil, false, nil
	}
	return &binding, true, nil
}

func emptyFiles(dir string, names map[string]bool) bool {
	for name := range names {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() != 0 {
			return false
		}
	}
	return true
}

// migrateLegacyEntry imports dir's mirror, when it has one, and removes dir,
// holding every legacy lock so no pre-v2 process is using it.
func (c *Cache) migrateLegacyEntry(ctx context.Context, dir string, binding *legacyBinding) (err error) {
	release, err := lockLegacyEntry(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	mirror := filepath.Join(dir, "mirror.git")
	imported := "nothing to import"
	if _, statErr := os.Stat(mirror); statErr == nil {
		if imported, err = c.importLegacyMirror(ctx, mirror, binding); err != nil {
			return err
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	size, err := dirSize(dir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove legacy git cache: %w", err)
	}
	logger.GetLogger("git").Infof("removed legacy git cache %s (%d bytes): %s", dir, size, imported)
	return nil
}

// lockLegacyEntry takes, without waiting, the exclusive lock of every legacy
// lock file in dir: the mirror, the binding and each worktree lease.
func lockLegacyEntry(dir string) (func() error, error) {
	leases, err := filepath.Glob(filepath.Join(dir, "worktrees", "*.lock"))
	if err != nil {
		return nil, err
	}
	var held []*flock.Flock
	release := func() error {
		var err error
		for _, lock := range held {
			err = errors.Join(err, lock.Unlock())
		}
		return err
	}
	for _, path := range append([]string{filepath.Join(dir, "mirror.lock"), filepath.Join(dir, "repository.lock")}, leases...) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		lock := flock.New(path)
		locked, err := lock.TryLock()
		if err != nil || !locked {
			return nil, errors.Join(fmt.Errorf("legacy git cache lock %s is held", path), err, release())
		}
		held = append(held, lock)
	}
	return release, nil
}

// importLegacyMirror fetches every ref of the legacy mirror from disk into
// the v2 pool of its repository, or its v2 mirror when it has no pool, under
// the same locks a v2 fetch takes.
func (c *Cache) importLegacyMirror(ctx context.Context, mirror string, binding *legacyBinding) (string, error) {
	if binding == nil {
		return "", errors.New("legacy mirror has no repository binding")
	}
	key := filepath.Base(filepath.Dir(mirror))
	parsed, err := ParseRepoURL(binding.Repository, "")
	if err != nil {
		return "", err
	}
	if parsed.CacheKey() != key {
		return "", fmt.Errorf("legacy key %s is not the cache key %s of %s", key, parsed.CacheKey(), binding.Repository)
	}
	shallow, err := isShallow(ctx, mirror)
	if err != nil {
		return "", err
	}
	if shallow {
		return "", errors.New("legacy mirror is shallow; only complete histories are imported")
	}
	repo := &Repository{URL: parsed}
	paths, err := c.mirrorPaths(repo)
	if err != nil {
		return "", err
	}
	return paths.importRefs(ctx, repo, mirror)
}

// importRefs fetches every ref of source into the pool as tips of this
// mirror's repository, or into the mirror under refs/commons-legacy/.
func (m mirrorPaths) importRefs(ctx context.Context, repo *Repository, source string) (_ string, err error) {
	unlock, err := m.lockWrite(ctx)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := m.ensure(ctx, repo); err != nil {
		return "", err
	}
	target, namespace := m.git, "refs/commons-legacy/"
	if m.pool != "" {
		lock, err := acquire(ctx, m.poolFetchLock(), "object pool fetch", exclusiveLock)
		if err != nil {
			return "", err
		}
		defer func() { err = errors.Join(err, lock.Unlock()) }()
		if err := requireComplete(m.pool); err != nil {
			return "", err
		}
		target, namespace = m.pool, poolTips+filepath.Base(m.dir)+"/legacy/"
	}
	if err := fetchLocal(ctx, target, source, "+refs/*:"+namespace+"*"); err != nil {
		return "", err
	}
	return "imported into " + target + " under " + namespace, nil
}
