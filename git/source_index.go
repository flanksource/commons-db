package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func (s *PreparedSource) captureIndex(ctx context.Context) error {
	root := s.info.RepositoryRoot
	index, err := run(ctx, command{dir: root, args: []string{"rev-parse", "--path-format=absolute", "--git-path", "index"}})
	if err != nil {
		return err
	}
	private := filepath.Join(s.gitDir, "index")
	content, err := os.ReadFile(index)
	if errors.Is(err, os.ErrNotExist) {
		if _, err = s.git(ctx, "read-tree", "--empty"); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		if err := os.WriteFile(private, content, 0o600); err != nil {
			return err
		}
		shared, err := run(ctx, command{dir: root, env: append(baseEnv(), "GIT_INDEX_FILE="+private),
			args: []string{"--no-optional-locks", "rev-parse", "--shared-index-path"}})
		if err != nil {
			return err
		}
		if shared != "" {
			if !filepath.IsAbs(shared) {
				shared = filepath.Join(root, shared)
			}
			backing, err := os.ReadFile(shared)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(s.gitDir, filepath.Base(shared)), backing, 0o600); err != nil {
				return err
			}
		}
	}
	unmerged, err := s.git(ctx, "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	if unmerged != "" {
		return ErrUnmergedIndex
	}
	if _, err := s.git(ctx, "-c", "core.splitIndex=false", "update-index", "--no-split-index"); err != nil {
		return err
	}
	s.info.TreeID, err = s.git(ctx, "write-tree")
	if err != nil {
		return err
	}
	head, err := sourceHead(ctx, root)
	if err != nil {
		return err
	}
	if head != s.info.BaseCommit {
		return ErrSourceChanged
	}
	return nil
}
