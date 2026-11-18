package git

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type mirrorRefsRead struct {
	repo *Repository
	refs []Ref
}

// lockMixedRefs borrows each source mirror's objects for the lifetime of one
// comparison. It prepares all sources before taking their read locks.
func (c *Cache) lockMixedRefs(ctx context.Context, options refReadOptions, resolved []Ref) (_ string, _ []string, _ func() error, err error) {
	sources := map[string]mirrorRefsRead{}
	shas := make([]string, len(resolved))
	for i, ref := range resolved {
		repo := options.Refs[i].repo
		key := repo.URL.CacheKey()
		source := sources[key]
		sources[key] = mirrorRefsRead{repo: repo, refs: append(source.refs, ref)}
		shas[i] = ref.SHA
	}
	keys := slices.Sorted(maps.Keys(sources))
	for _, key := range keys {
		source := sources[key]
		_, _, unlock, err := c.lockResolvedRefs(ctx, source.repo, source.refs, true)
		if err != nil {
			return "", nil, nil, err
		}
		if err := unlock(); err != nil {
			return "", nil, nil, err
		}
	}
	scratch, err := os.MkdirTemp(c.cacheRoot(), ".comparison-")
	if err != nil {
		return "", nil, nil, err
	}
	var releases []func() error
	closeRead := func() error {
		var cleanup error
		for _, release := range releases {
			cleanup = errors.Join(cleanup, release())
		}
		return errors.Join(cleanup, os.RemoveAll(scratch))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeRead())
		}
	}()
	objects, err := c.lockMixedObjects(ctx, sources, keys, &releases)
	if err != nil {
		return "", nil, nil, err
	}
	if err := ensureMirror(ctx, scratch); err != nil {
		return "", nil, nil, err
	}
	if err := replaceFile(filepath.Join(scratch, "objects", "info", "alternates"), []byte(strings.Join(objects, "\n")+"\n")); err != nil {
		return "", nil, nil, err
	}
	return scratch, shas, closeRead, nil
}

func (c *Cache) lockMixedObjects(ctx context.Context, sources map[string]mirrorRefsRead, keys []string, releases *[]func() error) ([]string, error) {
	var objects []string
	for _, key := range keys {
		paths := newMirrorPaths(filepath.Join(c.cacheRoot(), key))
		lock, err := acquire(ctx, paths.lock, "comparison mirror lock", sharedLock)
		if err != nil {
			return nil, err
		}
		*releases = append(*releases, lock.Unlock)
		for _, ref := range sources[key].refs {
			if !hasCommit(ctx, paths.git, ref.SHA) {
				return nil, fmt.Errorf("comparison commit %s was evicted from %s", ref.SHA, key)
			}
		}
		dir, err := filepath.Abs(filepath.Join(paths.git, "objects"))
		if err != nil {
			return nil, err
		}
		objects = append(objects, dir)
	}
	return objects, nil
}
