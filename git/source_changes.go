package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (s *PreparedSource) treeChanges(ctx context.Context) ([]SourceChange, error) {
	base := s.info.BaseCommit
	if base == "" {
		var err error
		base, err = s.git(ctx, "mktree")
		if err != nil {
			return nil, err
		}
	}
	raw, err := s.git(ctx, "diff-tree", "--no-commit-id", "-r", "--no-renames", "--no-ext-diff", "--no-textconv", "--name-status", "-z", base, s.info.TreeID, "--")
	if err != nil {
		return nil, err
	}
	return parseSourceChanges(raw)
}

func sourceWorkingChanges(ctx context.Context, info SourceInfo) ([]SourceChange, error) {
	args := []string{"--no-lazy-fetch", "--no-optional-locks", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--name-status", "-z", info.BaseCommit, "--"}
	if info.BaseCommit == "" {
		args = []string{"--no-optional-locks", "ls-files", "--cached", "-z"}
	}
	raw, err := run(ctx, command{dir: info.RepositoryRoot, args: args})
	if err != nil {
		return nil, err
	}
	changes := []SourceChange{}
	if info.BaseCommit != "" {
		if changes, err = parseSourceChanges(raw); err != nil {
			return nil, err
		}
	} else {
		for _, path := range strings.Split(raw, "\x00") {
			if path != "" {
				if _, err := os.Lstat(filepath.Join(info.Dir, path)); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return nil, err
				}
				changes = append(changes, SourceChange{Path: path, Status: FileAdded})
			}
		}
	}
	untracked, err := run(ctx, command{dir: info.RepositoryRoot, args: []string{"--no-optional-locks", "ls-files", "--others", "--exclude-standard", "-z"}})
	if err != nil {
		return nil, err
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path != "" {
			changes = append(changes, SourceChange{Path: path, Status: FileAdded})
		}
	}
	slices.SortFunc(changes, func(a, b SourceChange) int { return strings.Compare(a.Path, b.Path) })
	return slices.CompactFunc(changes, func(a, b SourceChange) bool { return a.Path == b.Path }), nil
}

func parseSourceChanges(raw string) ([]SourceChange, error) {
	changes := []SourceChange{}
	parts := strings.Split(raw, "\x00")
	if parts[len(parts)-1] != "" {
		return nil, fmt.Errorf("unterminated Git source changes")
	}
	parts = parts[:len(parts)-1]
	if len(parts)%2 != 0 {
		return nil, fmt.Errorf("incomplete Git source change")
	}
	for i := 0; i < len(parts); i += 2 {
		status := FileStatus(parts[i])
		switch status {
		case FileAdded, FileModified, FileDeleted, FileTypeChanged:
		case "U":
			return nil, ErrUnmergedIndex
		default:
			return nil, fmt.Errorf("unsupported Git source status %q", status)
		}
		if _, err := safeJoin("/source", parts[i+1]); err != nil {
			return nil, err
		}
		changes = append(changes, SourceChange{Path: parts[i+1], Status: status})
	}
	slices.SortFunc(changes, func(a, b SourceChange) int { return strings.Compare(a.Path, b.Path) })
	return changes, nil
}
