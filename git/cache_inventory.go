package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// cacheRefPrefix holds, per repository a mirror serves, the commits and
// branches recorded for it (recordCacheRef).
const cacheRefPrefix = "refs/commons-cache/repos/"

// inventoryCommits is how many recorded commits an entry lists.
const inventoryCommits = 5

// CacheEntry is one repository's directory under Cache.Root. Owner is the
// cache key whose mirror holds its objects (Key itself for a mirror it owns);
// Error reports an entry that could not be read, with the rest left empty.
type CacheEntry struct {
	Key        string          `json:"key"`
	Repository string          `json:"repository,omitempty"`
	Owner      string          `json:"owner"`
	Pool       string          `json:"pool,omitempty"`
	BoundAt    *time.Time      `json:"boundAt,omitempty"`
	Dependents []string        `json:"dependents,omitempty"`
	Mirror     *MirrorState    `json:"mirror,omitempty"`
	Commits    []CachedCommit  `json:"commits,omitempty"`
	Worktrees  []WorktreeState `json:"worktrees,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// MirrorState describes a mirror an entry owns. Locked is true while a fetch,
// pin write or maintenance holds the mirror exclusively; LastUsed and
// OldestUse are the newest and oldest pin uses.
type MirrorState struct {
	SizeBytes int64      `json:"sizeBytes"`
	Objects   int64      `json:"objects"`
	Packs     int64      `json:"packs"`
	Shallow   bool       `json:"shallow"`
	Pins      int        `json:"pins"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
	OldestUse *time.Time `json:"oldestUse,omitempty"`
	Locked    bool       `json:"locked"`
}

// CachedCommit is a commit recorded for an entry in its owner's mirror: Ref is
// heads/<branch> or commits/<sha>.
type CachedCommit struct {
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Committed time.Time `json:"committed"`
}

// WorktreeState is a persistent or ephemeral worktree; Leased while a
// Checkout holds it.
type WorktreeState struct {
	Name   string `json:"name"`
	Leased bool   `json:"leased"`
}

// Inventory describes every cache entry under Root without taking any lock a
// read or fetch waits on, sorted by key.
func (c *Cache) Inventory(ctx context.Context) ([]CacheEntry, error) {
	keys, err := c.cacheKeys()
	if err != nil {
		return nil, err
	}
	entries := make([]CacheEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, c.inventoryEntry(ctx, key))
	}
	return entries, ctx.Err()
}

// cacheKeys lists the directories of Root holding a mirror or a binding.
func (c *Cache) cacheKeys() ([]string, error) {
	if c.Root == "" {
		return nil, errors.New("git cache root is required")
	}
	root := c.cacheRoot()
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read git cache %s: %w", c.Root, err)
	}
	var keys []string
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, dir.Name(), "mirror.git", "HEAD")); err == nil {
			keys = append(keys, dir.Name())
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (c *Cache) inventoryEntry(ctx context.Context, key string) CacheEntry {
	entry := CacheEntry{Key: key, Owner: key}
	fail := func(err error) CacheEntry {
		entry.Error = Redact(err.Error())
		return entry
	}
	paths := newMirrorPaths(filepath.Join(c.cacheRoot(), key))
	meta, err := paths.metadata()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	entry.Repository, entry.Pool = meta.Repository, meta.Pool
	if info, err := os.Stat(paths.metadataPath()); err == nil {
		bound := info.ModTime().UTC()
		entry.BoundAt = &bound
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	if entry.Mirror, err = mirrorState(ctx, paths); err != nil {
		return fail(err)
	}
	if entry.Commits, err = cachedCommits(ctx, paths.git, key); err != nil {
		return fail(err)
	}
	if entry.Worktrees, err = worktreeStates(filepath.Join(paths.dir, "worktrees")); err != nil {
		return fail(err)
	}
	return entry
}

func mirrorState(ctx context.Context, paths mirrorPaths) (*MirrorState, error) {
	out, err := run(ctx, command{dir: paths.git, args: []string{"count-objects", "-v"}})
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, line := range lines(out) {
		name, value, ok := strings.Cut(line, ": ")
		if !ok {
			return nil, fmt.Errorf("unexpected count-objects line %q in %s", line, paths.git)
		}
		if name == "alternate" || name == "warning" {
			continue
		}
		if counts[name], err = strconv.ParseInt(value, 10, 64); err != nil {
			return nil, fmt.Errorf("unexpected count-objects line %q in %s", line, paths.git)
		}
	}
	state := &MirrorState{SizeBytes: (counts["size"] + counts["size-pack"]) * 1024, Objects: counts["count"] + counts["in-pack"], Packs: counts["packs"]}
	if _, err := os.Stat(filepath.Join(paths.git, "shallow")); err == nil {
		state.Shallow = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if state.Locked, err = heldExclusively(paths.lock); err != nil {
		return nil, err
	}
	pins, err := os.ReadDir(paths.pins)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, pin := range pins {
		info, err := pin.Info()
		if err != nil {
			return nil, err
		}
		used := info.ModTime().UTC()
		state.Pins++
		if state.LastUsed == nil || used.After(*state.LastUsed) {
			state.LastUsed = &used
		}
		if state.OldestUse == nil || used.Before(*state.OldestUse) {
			state.OldestUse = &used
		}
	}
	return state, nil
}

// cachedCommits lists the newest commits recorded for key in mirror.
func cachedCommits(ctx context.Context, mirror, key string) ([]CachedCommit, error) {
	prefix := cacheRefPrefix + key + "/"
	out, err := run(ctx, command{dir: mirror, args: []string{"for-each-ref", "--sort=-committerdate", "--count=" + strconv.Itoa(inventoryCommits),
		"--format=%(refname)%00%(objectname)%00%(committerdate:unix)", prefix}})
	if err != nil {
		return nil, err
	}
	var commits []CachedCommit
	for _, line := range lines(out) {
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected for-each-ref line %q in %s", line, mirror)
		}
		unix, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected commit time %q of %s in %s", fields[2], fields[0], mirror)
		}
		commits = append(commits, CachedCommit{Ref: strings.TrimPrefix(fields[0], prefix), SHA: fields[1], Committed: time.Unix(unix, 0).UTC()})
	}
	return commits, nil
}

func worktreeStates(dir string) ([]WorktreeState, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var worktrees []WorktreeState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		leased, err := heldExclusively(filepath.Join(dir, entry.Name()+".lock"))
		if err != nil {
			return nil, err
		}
		worktrees = append(worktrees, WorktreeState{Name: entry.Name(), Leased: leased})
	}
	return worktrees, nil
}

// heldExclusively reports whether another holder has path's lock exclusively,
// without creating a missing lock file.
func heldExclusively(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	lock := flock.New(path)
	locked, err := lock.TryRLock()
	if err != nil {
		return false, fmt.Errorf("probe lock %s: %w", path, err)
	}
	if !locked {
		return true, nil
	}
	return false, lock.Unlock()
}
