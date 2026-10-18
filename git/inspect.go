package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Ported from oipa-cli internal/gitcheckout/inspect.go.

var ErrNotRepository = errors.New("not a git repository")

// InspectOptions carries the requested URL and ref, which a detached cache
// worktree cannot report itself.
type InspectOptions struct {
	RepositoryHint string
	BranchHint     string
}

type DirtyStats struct {
	Staged    int
	Unstaged  int
	Untracked int
	Conflicts int
}

func (s DirtyStats) Clean() bool {
	return s.Staged == 0 && s.Unstaged == 0 && s.Untracked == 0 && s.Conflicts == 0
}

type LastCommit struct {
	Subject     string
	Author      string
	CommittedAt time.Time
}

type Metadata struct {
	Repository string
	Branch     string
	SHA        string
	Dirty      DirtyStats
	LastCommit LastCommit
}

// Inspect reads compact provenance of the checkout at dir. Hints preserve the
// user's requested URL/ref for detached cache worktrees, while every value
// read from git describes the exact checked-out tree.
func Inspect(ctx context.Context, dir string, options InspectOptions) (Metadata, error) {
	root, err := run(ctx, command{dir: dir, args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	sha, err := run(ctx, command{dir: dir, args: []string{"rev-parse", "--short=12", "HEAD"}})
	if err != nil {
		return Metadata{}, err
	}
	status, err := run(ctx, command{dir: dir, args: []string{"status", "--porcelain=v1", "--untracked-files=normal"}})
	if err != nil {
		return Metadata{}, err
	}
	last, err := run(ctx, command{dir: dir, args: []string{"log", "-1", "--format=%an%x00%cI%x00%s"}})
	if err != nil {
		return Metadata{}, err
	}
	commit, err := parseLastCommit(last)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{
		Repository: sanitizeRepository(inspectRepository(ctx, dir, root, options.RepositoryHint)),
		Branch:     inspectBranch(ctx, dir, options.BranchHint),
		SHA:        sha,
		Dirty:      parseDirtyStats(status),
		LastCommit: commit,
	}, nil
}

// inspectBranch is the hint, else the checked-out branch, else "detached"
// (symbolic-ref fails exactly when HEAD is detached).
func inspectBranch(ctx context.Context, dir, hint string) string {
	if branch := strings.TrimSpace(hint); branch != "" {
		return branch
	}
	if branch, err := run(ctx, command{dir: dir, args: []string{"symbolic-ref", "--quiet", "--short", "HEAD"}}); err == nil && branch != "" {
		return branch
	}
	return "detached"
}

// inspectRepository is the hint, else the origin URL, else the checkout root
// (config --get fails exactly when origin has no URL).
func inspectRepository(ctx context.Context, dir, root, hint string) string {
	if repository := strings.TrimSpace(hint); repository != "" {
		return repository
	}
	if origin, err := run(ctx, command{dir: dir, args: []string{"config", "--get", "remote.origin.url"}}); err == nil && origin != "" {
		return origin
	}
	return root
}

func parseDirtyStats(status string) DirtyStats {
	var stats DirtyStats
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		switch {
		case x == '?' && y == '?':
			stats.Untracked++
		case isConflictStatus(x, y):
			stats.Conflicts++
		default:
			if x != ' ' {
				stats.Staged++
			}
			if y != ' ' {
				stats.Unstaged++
			}
		}
	}
	return stats
}

func isConflictStatus(x, y byte) bool {
	switch string([]byte{x, y}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func parseLastCommit(value string) (LastCommit, error) {
	parts := strings.SplitN(value, "\x00", 3)
	if len(parts) != 3 {
		return LastCommit{}, fmt.Errorf("parse git last commit: expected author, time, and subject")
	}
	committedAt, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return LastCommit{}, fmt.Errorf("parse git commit time %q: %w", parts[1], err)
	}
	return LastCommit{Author: parts[0], CommittedAt: committedAt.UTC(), Subject: parts[2]}, nil
}

func sanitizeRepository(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		return parsed.String()
	}
	// SCP-style Git URLs use user@host:path. Remove the user component while
	// leaving ordinary local paths containing '@' untouched.
	if at, colon := strings.IndexByte(value, '@'), strings.IndexByte(value, ':'); at >= 0 && colon > at {
		return value[at+1:]
	}
	return value
}
