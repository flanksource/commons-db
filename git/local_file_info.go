package git

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

type LocalFileOptions struct {
	Directory string
	Revision  string
	Path      string
}

type FileCommit struct {
	SHA         string
	CommittedAt time.Time
	Present     bool
}

type LocalFileInfo struct {
	Commit FileCommit
	Dirty  bool
}

// LocalFile reads live path status and caches committed provenance independently.
func (c *Cache) LocalFile(ctx context.Context, options LocalFileOptions) (LocalFileInfo, error) {
	if !fullSHAPattern.MatchString(options.Revision) || !fs.ValidPath(options.Path) || options.Path == "." {
		return LocalFileInfo{}, fmt.Errorf("local file provenance requires a full commit SHA and relative file path")
	}
	found, err := Discover(ctx, DiscoverOptions{Path: options.Directory})
	if err != nil {
		return LocalFileInfo{}, err
	}
	if found.Bare {
		return LocalFileInfo{}, fmt.Errorf("local file status requires a working checkout")
	}
	status, err := run(ctx, command{dir: found.Root, args: []string{"--literal-pathspecs", "status", "--porcelain", "--", options.Path}})
	if err != nil {
		return LocalFileInfo{}, err
	}
	repo, err := NewRepository(RepositoryOptions{URL: found.Root})
	if err != nil {
		return LocalFileInfo{}, err
	}
	commit, err := cachedRead(ctx, c.Root, repo, "file-commit", []string{options.Revision, options.Path}, func(ctx context.Context) (FileCommit, error) {
		last, err := run(ctx, command{dir: found.Root, args: []string{"--literal-pathspecs", "log", "-1", "--follow", "--format=%cI%x00%H", "--end-of-options", options.Revision, "--", options.Path}})
		if err != nil {
			return FileCommit{}, err
		}
		if last == "" {
			return FileCommit{}, nil
		}
		date, sha, ok := strings.Cut(last, "\x00")
		if !ok || !fullSHAPattern.MatchString(sha) {
			return FileCommit{}, fmt.Errorf("invalid Git history for %s", options.Path)
		}
		at, err := time.Parse(time.RFC3339, date)
		return FileCommit{SHA: sha, CommittedAt: at.UTC(), Present: true}, err
	})
	return LocalFileInfo{Commit: commit, Dirty: status != ""}, err
}
