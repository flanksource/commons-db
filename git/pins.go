package git

import (
	"context"
	"errors"
	"fmt"
	registry "github.com/flanksource/commons-db/git/internal/cache"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pinRefPrefix holds one ref per commit a read retained, so gc keeps it.
const pinRefPrefix = "refs/commons-cache/commits/"

// pinnedCommits is, per mirror (by absolute path), the commits this process
// has seen present and pinned there, so a repeat read skips cat-file and
// update-ref. Maintain forgets the pins it expires; a pin another process
// expired is noticed through its missing pin file.
var pinnedCommits sync.Map

// mirrorPaths locates one repository's cache directory:
//
//	<dir>/mirror.git      the bare mirror
//	<dir>/mirror.lock     shared by reads, exclusive for fetch and ref writes
//	<dir>/pins/<sha>      the pin of <sha>; its mtime is when a read last used it
type mirrorPaths struct {
	dir, git, lock, pins string
}

func newMirrorPaths(dir string) mirrorPaths {
	return mirrorPaths{dir: dir, git: filepath.Join(dir, "mirror.git"), lock: filepath.Join(dir, "mirror.lock"), pins: filepath.Join(dir, "pins")}
}

func (c *Cache) mirrorPaths(repo *Repository) (mirrorPaths, error) {
	if c.Root == "" {
		return mirrorPaths{}, errors.New("git cache root is required")
	}
	if repo == nil {
		return mirrorPaths{}, errors.New("git repository is required")
	}
	owner, err := registry.Load(c.Root, repo.URL.CacheKey())
	if err != nil {
		return mirrorPaths{}, err
	}
	return newMirrorPaths(filepath.Join(c.Root, owner)), nil
}

func (m mirrorPaths) pinned() (*sync.Map, error) {
	key, err := filepath.Abs(m.git)
	if err != nil {
		return nil, err
	}
	set, _ := pinnedCommits.LoadOrStore(key, &sync.Map{})
	return set.(*sync.Map), nil
}

// usePinned reports whether every commit is known pinned in this process and
// still pinned on disk, refreshing each pin's last use. A History read also
// needs a mirror that is not shallow.
func (m mirrorPaths) usePinned(shas []string, history bool) (bool, error) {
	if history {
		if _, err := os.Stat(filepath.Join(m.git, "shallow")); !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	set, err := m.pinned()
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, sha := range shas {
		if _, known := set.Load(sha); !known {
			return false, nil
		}
		err := os.Chtimes(filepath.Join(m.pins, sha), now, now)
		if errors.Is(err, fs.ErrNotExist) {
			set.Delete(sha)
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("refresh pin of %s: %w", sha, err)
		}
	}
	return true, nil
}

// pin writes the pin refs and pin files of shas; the caller holds the
// exclusive mirror lock.
func (m mirrorPaths) pin(ctx context.Context, shas []string) error {
	var updates strings.Builder
	for _, sha := range shas {
		fmt.Fprintf(&updates, "update %s%s %s\n", pinRefPrefix, sha, sha)
	}
	if _, err := run(ctx, command{dir: m.git, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())}); err != nil {
		return err
	}
	if err := os.MkdirAll(m.pins, 0o755); err != nil {
		return fmt.Errorf("create pin directory: %w", err)
	}
	set, err := m.pinned()
	if err != nil {
		return err
	}
	for _, sha := range shas {
		if err := os.WriteFile(filepath.Join(m.pins, sha), nil, 0o644); err != nil {
			return fmt.Errorf("record pin of %s: %w", sha, err)
		}
		set.Store(sha, true)
	}
	return nil
}
