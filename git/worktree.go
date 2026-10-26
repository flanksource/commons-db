package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// Worktree is a leased checkout from a Cache. Release it when done.
type Worktree struct {
	Dir string
	Ref Ref

	repo       *Repository
	mirror     string
	mirrorLock string
	lease      *flock.Flock
	ephemeral  bool
	branches   []string
	tags       []string
}

// Signature is a commit author or tag tagger.
type Signature struct {
	Name  string
	Email string
}

func (s Signature) env() ([]string, error) {
	if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Email) == "" {
		return nil, errors.New("git signature needs a name and an email")
	}
	return []string{"GIT_AUTHOR_NAME=" + s.Name, "GIT_AUTHOR_EMAIL=" + s.Email,
		"GIT_COMMITTER_NAME=" + s.Name, "GIT_COMMITTER_EMAIL=" + s.Email}, nil
}

// committerEnv is the isolated environment with s as committer only, so a
// replayed commit (cherry-pick) keeps its original author.
func (s Signature) committerEnv() ([]string, error) {
	if _, err := s.env(); err != nil {
		return nil, err
	}
	return append(baseEnv(), "GIT_COMMITTER_NAME="+s.Name, "GIT_COMMITTER_EMAIL="+s.Email), nil
}

// CommitOptions configures Worktree.Commit.
type CommitOptions struct {
	Message string
	Author  Signature
}

// TagOptions configures Worktree.CreateTag; an empty Message makes a
// lightweight tag.
type TagOptions struct {
	Name    string
	Message string
	Tagger  Signature
}

// Release gives the worktree lease back. An ephemeral worktree is removed
// from disk and the mirror, with the branches and tags it created.
func (w *Worktree) Release() error {
	if !w.ephemeral {
		return w.unlease()
	}
	return errors.Join(w.remove(), w.unlease())
}

func (w *Worktree) remove() (err error) {
	ctx := context.Background()
	lock, err := acquire(ctx, w.mirrorLock, "mirror lock", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if _, err := run(ctx, command{dir: w.mirror, args: []string{"worktree", "remove", "--force", "--end-of-options", w.Dir}}); err != nil {
		return err
	}
	if len(w.branches) > 0 {
		if _, err := run(ctx, command{dir: w.mirror, args: append([]string{"branch", "--quiet", "-D", "--end-of-options"}, w.branches...)}); err != nil {
			return err
		}
	}
	if len(w.tags) > 0 {
		_, err = run(ctx, command{dir: w.mirror, args: append([]string{"tag", "--delete", "--end-of-options"}, w.tags...)})
	}
	return err
}

func (w *Worktree) unlease() error {
	if err := w.lease.Unlock(); err != nil {
		return fmt.Errorf("release worktree lease %s: %w", w.lease.Path(), err)
	}
	if w.ephemeral {
		if err := os.Remove(w.lease.Path()); err != nil {
			return fmt.Errorf("remove worktree lease %s: %w", w.lease.Path(), err)
		}
	}
	return nil
}

// CreateBranch creates name at the current commit and switches to it.
func (w *Worktree) CreateBranch(ctx context.Context, name string) error {
	if err := ValidateBranchName(name); err != nil {
		return err
	}
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"checkout", "--quiet", "-b", name}}); err != nil {
		return err
	}
	w.branches = append(w.branches, name)
	return nil
}

// WriteFile writes content to the worktree-relative path, creating parents.
func (w *Worktree) WriteFile(path string, content []byte) error {
	target, err := safeJoin(w.Dir, path)
	if err != nil {
		return err
	}
	if err := ensureInside(w.Dir, existingAncestor(filepath.Dir(target))); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// DeleteFile removes a worktree-relative file; a missing file is an error.
func (w *Worktree) DeleteFile(path string) error {
	target, err := safeJoin(w.Dir, path)
	if err != nil {
		return err
	}
	if err := ensureInside(w.Dir, filepath.Dir(target)); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// Commit stages every change in the worktree and commits it.
func (w *Worktree) Commit(ctx context.Context, options CommitOptions) (string, error) {
	if strings.TrimSpace(options.Message) == "" {
		return "", errors.New("commit message is required")
	}
	identity, err := options.Author.env()
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, command{dir: w.Dir, args: []string{"add", "--all"}}); err != nil {
		return "", err
	}
	env := append(baseEnv(), identity...)
	if _, err := run(ctx, command{dir: w.Dir, env: env, stdin: []byte(options.Message), args: []string{"commit", "--quiet", "--file=-"}}); err != nil {
		return "", err
	}
	return run(ctx, command{dir: w.Dir, args: []string{"rev-parse", "--verify", "HEAD"}})
}

// CreateTag tags the current commit, annotated when Message is set.
func (w *Worktree) CreateTag(ctx context.Context, options TagOptions) error {
	if err := createTag(ctx, w.Dir, "HEAD", options); err != nil {
		return err
	}
	w.tags = append(w.tags, options.Name)
	return nil
}

// createTag tags target in the repository at dir, annotated when Message is
// set.
func createTag(ctx context.Context, dir, target string, options TagOptions) error {
	if err := ValidateTagName(options.Name); err != nil {
		return err
	}
	cmd := command{dir: dir, args: []string{"tag", "--end-of-options", options.Name, target}}
	if options.Message != "" {
		identity, err := options.Tagger.env()
		if err != nil {
			return err
		}
		cmd = command{dir: dir, env: append(baseEnv(), identity...), stdin: []byte(options.Message),
			args: []string{"tag", "--annotate", "--file=-", "--end-of-options", options.Name, target}}
	}
	_, err := run(ctx, cmd)
	return err
}

// safeJoin resolves a worktree-relative path, refusing absolute paths, ".."
// escapes and anything inside .git.
func safeJoin(root, path string) (string, error) {
	clean, err := cleanChangePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, clean), nil
}

// cleanChangePath is a worktree-relative path in the OS's form, refusing
// absolute paths, ".." escapes and anything inside .git.
func cleanChangePath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path %q must be relative to the worktree", path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the worktree", path)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("path %q is inside .git", path)
		}
	}
	return clean, nil
}

func existingAncestor(dir string) string {
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// ensureInside refuses a directory that resolves, through symlinks, outside
// root.
func ensureInside(root, dir string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve worktree %s: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return fmt.Errorf("%s resolves outside the worktree", dir)
	}
	return nil
}
