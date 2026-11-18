package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// MirrorStore keeps one bare mirror per repository on the shared path, in the
// local cache's layout:
//
//	<Path>/<key>/mirror.git   the store mirror
//	<Path>/<key>/mirror.lock  shared by hydrates, exclusive for a publish
//	<Path>/<key>/published    touched by every publish
//
// It suits a ReadWriteOnce volume or development; on NFS (EFS) every publish
// takes a file lock and rewrites refs there, which PackStore avoids.
type MirrorStore struct {
	Path string
}

var _ Store = (*MirrorStore)(nil)

// Hydrate fetches every ref of the store mirror into the local mirror under
// refs/commons-store/.
func (s *MirrorStore) Hydrate(ctx context.Context, key, mirror string) (err error) {
	dir, err := storeDir(s.Path, key)
	if err != nil {
		return err
	}
	paths := newMirrorPaths(dir)
	if _, err := os.Stat(filepath.Join(paths.git, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("git store mirror %s: %w", paths.git, err)
	}
	lock, err := acquire(ctx, paths.lock, "git store mirror lock", sharedLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	return fetchLocal(ctx, mirror, paths.git, "+refs/*:"+storeRefPrefix+"*")
}

// Publish fetches every ref of the local mirror, except the ones a hydrate
// brought in, into the store mirror under its exclusive lock.
func (s *MirrorStore) Publish(ctx context.Context, key, mirror string) (err error) {
	dir, err := storeDir(s.Path, key)
	if err != nil {
		return err
	}
	paths := newMirrorPaths(dir)
	lock, err := acquire(ctx, paths.lock, "git store mirror lock", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, paths.git); err != nil {
		return err
	}
	if err := fetchLocal(ctx, paths.git, mirror, "+refs/*:refs/*", "^"+storeRefPrefix+"*"); err != nil {
		return err
	}
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "published"), nil, 0o644); err != nil {
		return fmt.Errorf("record publish of %s: %w", key, err)
	}
	return os.Chtimes(filepath.Join(dir, "published"), now, now)
}

// Inventory describes every store mirror.
func (s *MirrorStore) Inventory(ctx context.Context) ([]StoreEntry, error) {
	keys, err := storeKeys(s.Path)
	if err != nil {
		return nil, err
	}
	entries := []StoreEntry{}
	for _, key := range keys {
		dir := filepath.Join(s.Path, cacheLayout, key)
		entry, err := mirrorStoreEntry(ctx, newMirrorPaths(dir))
		if err != nil {
			return nil, err
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}
	return entries, nil
}

func mirrorStoreEntry(ctx context.Context, paths mirrorPaths) (_ *StoreEntry, err error) {
	lock, err := acquire(ctx, paths.lock, "git store mirror lock", sharedLock)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if _, err := os.Stat(filepath.Join(paths.git, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("git store mirror %s: %w", paths.git, err)
	}
	entry := &StoreEntry{Key: filepath.Base(paths.dir)}
	if entry.SizeBytes, err = dirSize(paths.git); err != nil {
		return nil, fmt.Errorf("size git store mirror %s: %w", entry.Key, err)
	}
	entry.Published, err = modTime(filepath.Join(paths.dir, "published"))
	return entry, err
}

// fetchLocal fetches refspecs from the repository at source (a local path)
// into the repository at dir.
func fetchLocal(ctx context.Context, dir, source string, refspecs ...string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	transport := (&url.URL{Scheme: "file", Path: filepath.ToSlash(source)}).String()
	args := append([]string{"fetch", "--quiet", "--no-tags", "--no-auto-maintenance", "--end-of-options", transport}, refspecs...)
	_, err = run(ctx, command{dir: dir, args: args})
	return err
}

// modTime is the file's modification time, nil when it does not exist.
func modTime(path string) (*time.Time, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at := info.ModTime()
	return &at, nil
}
