package git

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/models"
)

const (
	cacheLayout  = "v2"
	poolTips     = "refs/commons-pool/tips/"
	poolRetained = "refs/commons-pool/retained/"
)

func (c *Cache) cacheRoot() string { return filepath.Join(c.Root, cacheLayout) }

func organizationPool(repo RepoURL) string {
	if repo.Scheme == "file" || repo.Host == "" {
		return ""
	}
	if repo.Provider == models.ConnectionTypeGit {
		provider, err := detectProvider(repo.Host, "")
		if err != nil || provider == models.ConnectionTypeGit {
			return ""
		}
		repo = RepoURL{Provider: provider, Host: repo.Host, Path: repo.Path}
		if err := parseRepoPath(&repo); err != nil {
			return ""
		}
	}
	host, organization := strings.ToLower(repo.Host), strings.ToLower(repo.Owner)
	if organization == "" {
		return ""
	}
	switch repo.Provider {
	case models.ConnectionTypeGithub:
		if host == "github.com:443" || host == "github.com:22" {
			host = "github.com"
		}
	case models.ConnectionTypeAzureDevops:
		if isAzureHost(hostOnly(host)) {
			host = "dev.azure.com"
		}
	default:
		return ""
	}
	identity := repo.Provider + "/" + host + "/" + organization
	readable := cacheKeyUnsafe.ReplaceAllString(identity, "_")
	return fmt.Sprintf("%s-%x", readable[:min(len(readable), 80)], sha256.Sum256([]byte(identity)))
}

type mirrorMetadata struct {
	Repository string `json:"repository"`
	Pool       string `json:"pool,omitempty"`
}

func (m mirrorPaths) metadataPath() string { return filepath.Join(m.dir, "repository.json") }

func (m mirrorPaths) metadata() (meta mirrorMetadata, err error) {
	data, err := os.ReadFile(m.metadataPath())
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("read git mirror metadata %s: %w", m.dir, err)
	}
	if meta.Pool != "" && !validCacheKey(meta.Pool) {
		return meta, fmt.Errorf("invalid object pool %q in %s", meta.Pool, m.dir)
	}
	return meta, nil
}

func (m mirrorPaths) poolName() string {
	if m.pool == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(m.pool))
}

// repoKey keys a repository's refs and stamp in the pool: its cache key,
// which is also its mirror directory's name.
func (m mirrorPaths) repoKey() string { return filepath.Base(m.dir) }

// lockWrite takes the locks a mirror write needs and returns the context the
// write runs under, cancelled if the pool lease is lost. Every path takes
// them in one order, so none waits on a lock while blocking the holder of an
// earlier one: the pool read lease (Maintain holds it exclusively), then
// mirror.lock (exclusive), then, only around a fetch into the pool, the pool
// fetch lease (exclusive for a repository the pool holds nothing of, else
// read).
func (m mirrorPaths) lockWrite(ctx context.Context) (context.Context, func() error, error) {
	work, unlockPool := ctx, func() error { return nil }
	if m.pool != "" {
		var err error
		work, unlockPool, err = holdLease(ctx, "git object pool "+m.poolName()+" read lease", m.locks.ReadWriteLock(m.poolLease(""), m.holder).RLock)
		if err != nil {
			return nil, nil, err
		}
	}
	lock, err := acquire(work, m.lock, "mirror lock", exclusiveLock)
	if err != nil {
		return nil, nil, errors.Join(err, unlockPool())
	}
	return work, func() error { return errors.Join(lock.Unlock(), unlockPool()) }, nil
}

func (m mirrorPaths) ensure(ctx context.Context, repo *Repository) error {
	if err := ensureMirror(ctx, m.git); err != nil {
		return err
	}
	want := mirrorMetadata{Repository: repo.URL.Redacted(), Pool: m.poolName()}
	if current, err := m.metadata(); err == nil {
		if current != want {
			return fmt.Errorf("git mirror metadata changed for %s", m.dir)
		}
		if m.pool != "" {
			return m.verifyAlternate()
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if m.pool != "" {
		if err := m.installAlternate(ctx); err != nil {
			return err
		}
	}
	data, err := json.Marshal(want)
	if err != nil {
		return err
	}
	return replaceFile(m.metadataPath(), data)
}

// installAlternate creates the pool when missing and points the mirror at
// it by absolute path, so every host mounting the pool at that path agrees.
func (m mirrorPaths) installAlternate(ctx context.Context) (err error) {
	ctx, release, err := holdLease(ctx, "git object pool "+m.poolName()+" init lease", m.locks.Mutex(m.poolLease("init"), m.holder).Lock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	if err := ensureMirror(ctx, m.pool); err != nil {
		return err
	}
	// Keep every fetch into the pool packed: a mirror's prune-packed (in
	// dropBorrowed) drops only the loose objects an alternate packs.
	if _, err := run(ctx, command{dir: m.pool, args: []string{"config", "fetch.unpackLimit", "1"}}); err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: m.git, args: []string{"config", "core.alternateRefsPrefixes", poolTips}}); err != nil {
		return err
	}
	objects, err := filepath.Abs(filepath.Join(m.pool, "objects"))
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(m.git, "objects", "info", "alternates"), []byte(objects+"\n"))
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func replaceFile(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".replace-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, removeIfExists(file.Name())) }()
	_, err = file.Write(data)
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// retain keeps pinned commits complete in the pool before the mirror publishes
// its pins. A ref-bearing tip is advertised only after the pool has its graph.
// Objects copied into the pool are then dropped from the mirror
// (dropBorrowed), so they are stored once. The refs are keyed by repository
// and refresh its stamp, which keeps them from pool maintenance on any host.
func (m mirrorPaths) retain(ctx context.Context, shas []string, advertise bool) error {
	if m.pool == "" || len(shas) == 0 {
		return nil
	}
	key := m.repoKey()
	if err := writeStamp(m.pool, key, time.Now()); err != nil {
		return err
	}
	var updates strings.Builder
	var specs []string
	for _, sha := range slices.Compact(slices.Sorted(slices.Values(shas))) {
		if !fullSHAPattern.MatchString(sha) {
			return fmt.Errorf("retain %q: a full commit SHA is required", sha)
		}
		ref := poolRetained + key + "/" + sha
		if !hasCommit(ctx, m.pool, sha) {
			specs = append(specs, "+"+sha+":"+ref)
		} else {
			fmt.Fprintf(&updates, "update %s %s\n", ref, sha)
		}
		if advertise {
			fmt.Fprintf(&updates, "update %s%s/%s %s\n", poolTips, key, sha, sha)
		}
	}
	if len(specs) > 0 {
		absolute, err := filepath.Abs(m.git)
		if err != nil {
			return err
		}
		source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
		args := append([]string{"fetch", "--quiet", "--atomic", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--end-of-options", source}, specs...)
		if _, err := run(ctx, command{dir: m.pool, args: args}); err != nil {
			return err
		}
	}
	if updates.Len() > 0 {
		if _, err := run(ctx, command{dir: m.pool, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())}); err != nil {
			return err
		}
	}
	if len(specs) == 0 {
		return nil
	}
	return m.dropBorrowed(ctx)
}

// dropBorrowed repacks the mirror without the objects its pool holds
// (repack -l), deleting the packs and loose objects it replaces (-d), so an
// object retained in the pool is not stored twice. The caller holds the
// exclusive mirror lock, so no mirror write races it. Readers that do not
// hold the lock are safe the way they are with git gc: an open pack stays
// readable after it is unlinked, and a reader that misses a removed pack
// rescans and finds the object in the pool. -A keeps unreachable objects
// (a commit fetched only into FETCH_HEAD for a worktree's cherry-pick) as
// loose objects for gc's prune expiry rather than deleting them.
func (m mirrorPaths) dropBorrowed(ctx context.Context) error {
	_, err := run(ctx, command{dir: m.git, args: []string{"-c", "repack.writeBitmaps=false", "repack", "-A", "-d", "-l", "-q"}})
	return err
}
