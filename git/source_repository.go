package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func (s *PreparedSource) git(ctx context.Context, args ...string) (string, error) {
	return run(ctx, command{dir: s.gitDir, env: s.env, args: args})
}

func (s *PreparedSource) initialize(ctx context.Context, cacheRoot string) error {
	absolute, err := filepath.Abs(cacheRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return err
	}
	if s.scratch, err = os.MkdirTemp(absolute, "source-"); err != nil {
		return err
	}
	s.gitDir = filepath.Join(s.scratch, "repository.git")
	s.info.Dir = filepath.Join(s.scratch, "tree")
	format, err := run(ctx, command{dir: s.info.RepositoryRoot, args: []string{"rev-parse", "--show-object-format"}})
	if err != nil {
		return err
	}
	_, err = run(ctx, command{dir: s.scratch, args: []string{"init", "--quiet", "--bare", "--template=", "--object-format=" + format, s.gitDir}})
	if err != nil {
		return err
	}
	objects, err := run(ctx, command{dir: s.info.RepositoryRoot, args: []string{"rev-parse", "--path-format=absolute", "--git-path", "objects"}})
	if err != nil {
		return err
	}
	s.env = append(baseEnv(), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strconv.Quote(objects), "GIT_NO_REPLACE_OBJECTS=1")
	return s.copyRefs(ctx)
}

func (s *PreparedSource) copyRefs(ctx context.Context) error {
	root := s.info.RepositoryRoot
	refs, err := run(ctx, command{dir: root, args: []string{"for-each-ref", "--format=%(objectname) %(refname)"}})
	if err != nil {
		return err
	}
	var packed strings.Builder
	for _, line := range strings.Split(refs, "\n") {
		if line == "" {
			continue
		}
		_, ref, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(ref, "refs/") {
			return fmt.Errorf("invalid local ref %q", line)
		}
		if strings.HasPrefix(ref, "refs/replace/") {
			continue
		}
		if _, err := safeJoin(s.gitDir, ref); err != nil {
			return err
		}
		packed.WriteString(line + "\n")
	}
	// Packed refs preserve case-distinct names on case-insensitive filesystems.
	if err := os.WriteFile(filepath.Join(s.gitDir, "packed-refs"), []byte(packed.String()), 0o600); err != nil {
		return err
	}
	head, err := sourceHead(ctx, root)
	if err != nil {
		return err
	}
	s.info.BaseCommit = head
	if head != "" {
		return os.WriteFile(filepath.Join(s.gitDir, "HEAD"), []byte(head+"\n"), 0o600)
	}
	return nil
}

func sourceHead(ctx context.Context, root string) (string, error) {
	head, err := run(ctx, command{dir: root, args: []string{"rev-parse", "--verify", "--quiet", "HEAD"}})
	if err == nil {
		return head, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return "", err
	}
	branch, branchErr := run(ctx, command{dir: root, args: []string{"symbolic-ref", "--quiet", "HEAD"}})
	if branchErr != nil {
		return "", errors.Join(err, branchErr)
	}
	_, branchErr = run(ctx, command{dir: root, args: []string{"show-ref", "--verify", "--quiet", branch}})
	if errors.As(branchErr, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return "", errors.Join(err, branchErr)
}

func (s *PreparedSource) resolveCommit(ctx context.Context, revision string) (string, error) {
	if err := ValidateRevision(revision); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	resolved, err := s.git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrRevisionNotFound, revision, err)
	}
	return resolved, nil
}

func (s *PreparedSource) resolveRevision(ctx context.Context, revision string) error {
	if err := ValidateRevision(revision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	separator := ".."
	if strings.Contains(revision, "...") {
		separator = "..."
	}
	left, right, ranged := strings.Cut(revision, separator)
	if !ranged {
		target, err := s.resolveCommit(ctx, revision)
		if err != nil {
			return err
		}
		s.info.TargetCommit = target
		parents, err := s.git(ctx, "rev-list", "--parents", "-n", "1", target, "--")
		if err != nil {
			return err
		}
		fields := strings.Fields(parents)
		if len(fields) == 0 {
			return fmt.Errorf("missing parent metadata for %s", target)
		}
		s.info.BaseCommit = ""
		if len(fields) > 1 {
			s.info.BaseCommit = fields[1]
		}
	} else {
		if left == "" {
			left = "HEAD"
		}
		if right == "" {
			right = "HEAD"
		}
		base, err := s.resolveCommit(ctx, left)
		if err != nil {
			return err
		}
		target, err := s.resolveCommit(ctx, right)
		if err != nil {
			return err
		}
		s.info.BaseCommit, s.info.TargetCommit = base, target
		if separator == "..." {
			if err := s.resolveMergeBase(ctx); err != nil {
				return err
			}
		}
	}
	var err error
	s.info.TreeID, err = s.git(ctx, "rev-parse", "--verify", s.info.TargetCommit+"^{tree}")
	return err
}

func (s *PreparedSource) resolveMergeBase(ctx context.Context) error {
	base, err := s.git(ctx, "merge-base", "--all", s.info.BaseCommit, s.info.TargetCommit)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return fmt.Errorf("%w: %w", ErrUnrelatedHistories, err)
	}
	if err != nil {
		return err
	}
	if len(strings.Fields(base)) != 1 {
		return fmt.Errorf("%w: multiple merge bases", ErrInvalidSource)
	}
	s.info.BaseCommit = base
	return nil
}
