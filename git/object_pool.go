package git

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

func (m mirrorPaths) poolLock() string { return filepath.Join(filepath.Dir(m.pool), "pool.lock") }

func (m mirrorPaths) lockWrite(ctx context.Context) (func() error, error) {
	unlockPool := func() error { return nil }
	if m.pool != "" {
		lock, err := acquire(ctx, m.poolLock(), "object pool", sharedLock)
		if err != nil {
			return nil, err
		}
		unlockPool = lock.Unlock
	}
	lock, err := acquire(ctx, m.lock, "mirror lock", exclusiveLock)
	if err != nil {
		return nil, errors.Join(err, unlockPool())
	}
	return func() error { return errors.Join(lock.Unlock(), unlockPool()) }, nil
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

func (m mirrorPaths) installAlternate(ctx context.Context) (err error) {
	lock, err := acquire(ctx, m.pool+".init.lock", "initialize object pool", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if err := ensureMirror(ctx, m.pool); err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: m.git, args: []string{"config", "core.alternateRefsPrefixes", poolTips}}); err != nil {
		return err
	}
	objects, err := filepath.Rel(filepath.Join(m.git, "objects"), filepath.Join(m.pool, "objects"))
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(m.git, "objects", "info", "alternates"), []byte(objects+"\n"))
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
func (m mirrorPaths) retain(ctx context.Context, shas []string, advertise bool) error {
	if m.pool == "" || len(shas) == 0 {
		return nil
	}
	key := filepath.Base(m.dir)
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
	if updates.Len() == 0 {
		return nil
	}
	_, err := run(ctx, command{dir: m.pool, args: []string{"update-ref", "--stdin"}, stdin: []byte(updates.String())})
	return err
}
