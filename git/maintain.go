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
// survives without a read using it.
type MaintainOptions struct {
	PinTTL time.Duration
}

// Maintain expires, in every mirror under Root, the commit pins no read has
// used for PinTTL, then runs git's gc task, each mirror under its exclusive
// lock. A pin's last use is its pin file's mtime, refreshed by every read
// (the ref itself points at the commit, whose date says nothing of use); a
// pin ref without a pin file has not been read since pin files existed and
// expires. A Tree keeps no pin fresh while open, so PinTTL must exceed the
// longest a Tree stays open.
func (c *Cache) Maintain(ctx context.Context, options MaintainOptions) error {
	if c.Root == "" {
		return errors.New("git cache root is required")
	}
	if options.PinTTL <= 0 {
		return fmt.Errorf("maintain git cache: pin TTL must be positive, got %s", options.PinTTL)
	}
	entries, err := os.ReadDir(c.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("maintain git cache %s: %w", c.Root, err)
	}
	cutoff := time.Now().Add(-options.PinTTL)
	for _, entry := range entries {
		paths := newMirrorPaths(filepath.Join(c.Root, entry.Name()))
		if _, err := os.Stat(filepath.Join(paths.git, "HEAD")); errors.Is(err, fs.ErrNotExist) {
			continue
		}
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
