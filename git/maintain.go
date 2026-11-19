package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaintainOptions configures Cache.Maintain. PinTTL is how long a commit pin
// survives without a read using it. Owners limits maintenance to those
// mirrors (cache keys owning a mirror); empty maintains every mirror.
type MaintainOptions struct {
	PinTTL time.Duration
	Owners []string
}

// Maintain expires, in every mirror under Root, the commit pins no read has
// used for PinTTL, then runs git's gc task, each mirror under its exclusive
// lock. A pin's last use is its pin file's mtime, refreshed by every read
// (the ref itself points at the commit, whose date says nothing of use); a
// pin ref without a pin file has not been read since pin files existed and
// expires. A Tree keeps no pin fresh while open, so PinTTL must exceed the
// longest a Tree stays open. A PackStore then compacts the bundles of every
// key it holds (of Owners only, when set). A full sweep (no Owners) first
// imports and removes the pre-v2 entries under Root (migrateLegacy); an
// import failure leaves that entry in place and fails Maintain after the rest
// of the sweep ran.
func (c *Cache) Maintain(ctx context.Context, options MaintainOptions) error {
	if err := c.maintainMirrors(ctx, options); err != nil {
		return err
	}
	if len(options.Owners) > 0 {
		return c.compactStore(ctx, options.Owners)
	}
	legacy := c.migrateLegacy(ctx)
	if err := c.maintainPools(ctx); err != nil {
		return errors.Join(legacy, err)
	}
	return errors.Join(legacy, c.compactStore(ctx, options.Owners))
}

// storeCompactor is a Store that folds its per-key files (PackStore).
type storeCompactor interface {
	Compact(ctx context.Context, key string) (bool, error)
}

func (c *Cache) compactStore(ctx context.Context, owners []string) error {
	compactor, ok := c.Store.(storeCompactor)
	if !ok {
		return nil
	}
	if len(owners) == 0 {
		entries, err := c.Store.Inventory(ctx)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			owners = append(owners, entry.Key)
		}
	}
	for _, owner := range owners {
		if _, err := compactor.Compact(ctx, owner); err != nil {
			return fmt.Errorf("compact git store %s: %w", owner, err)
		}
	}
	return nil
}

func (c *Cache) maintainMirrors(ctx context.Context, options MaintainOptions) error {
	if c.Root == "" {
		return errors.New("git cache root is required")
	}
	if options.PinTTL <= 0 {
		return fmt.Errorf("maintain git cache: pin TTL must be positive, got %s", options.PinTTL)
	}
	root, err := os.OpenRoot(c.cacheRoot())
	if errors.Is(err, fs.ErrNotExist) && len(options.Owners) == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("maintain git cache %s: %w", c.Root, err)
	}
	defer root.Close()
	owners := options.Owners
	if len(owners) == 0 {
		entries, err := os.ReadDir(c.cacheRoot())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("maintain git cache %s: %w", c.Root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := root.Stat(filepath.Join(entry.Name(), "mirror.git", "HEAD")); err == nil {
				owners = append(owners, entry.Name())
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("maintain git mirror %s: %w", entry.Name(), err)
			}
		}
	}
	cutoff := time.Now().Add(-options.PinTTL)
	for _, owner := range owners {
		if owner == "." || !filepath.IsLocal(owner) || owner != filepath.Base(owner) {
			return fmt.Errorf("maintain git cache: invalid mirror %q", owner)
		}
		if _, err := root.Stat(filepath.Join(owner, "mirror.git", "HEAD")); err != nil {
			return fmt.Errorf("maintain git mirror %s: %w", owner, err)
		}
		paths := newMirrorPaths(filepath.Join(c.cacheRoot(), owner))
		if err := maintainMirror(ctx, paths, cutoff); err != nil {
			return fmt.Errorf("maintain git mirror %s: %w", paths.git, err)
		}
	}
	return nil
}

func maintainMirror(ctx context.Context, paths mirrorPaths, cutoff time.Time) (err error) {
	lock, err := acquire(ctx, paths.lock, "mirror lock", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := expirePins(ctx, paths, cutoff); err != nil {
		return err
	}
	_, err = run(ctx, command{dir: paths.git, args: []string{"maintenance", "run", "--task=gc", "--quiet"}})
	return err
}

func expirePins(ctx context.Context, paths mirrorPaths, cutoff time.Time) error {
	refs, err := run(ctx, command{dir: paths.git, args: []string{"for-each-ref", "--format=%(refname)", pinRefPrefix}})
	if err != nil {
		return err
	}
	set, err := paths.pinned()
	if err != nil {
		return err
	}
	var deletes strings.Builder
	for _, ref := range lines(refs) {
		sha := strings.TrimPrefix(ref, pinRefPrefix)
		pin := filepath.Join(paths.pins, sha)
		info, err := os.Stat(pin)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil && !info.ModTime().Before(cutoff) {
			continue
		}
		set.Delete(sha)
		if err := os.Remove(pin); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		fmt.Fprintf(&deletes, "delete %s\n", ref)
	}
	if deletes.Len() == 0 {
		return nil
	}
	_, err = run(ctx, command{dir: paths.git, args: []string{"update-ref", "--stdin"}, stdin: []byte(deletes.String())})
	return err
}
