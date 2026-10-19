package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// VisiblePaths contains tracked and unignored files and their parent directories.
// Outside a Git checkout, every filesystem path is visible.
type VisiblePaths struct {
	directory  string
	paths      map[string]bool
	repository bool
}

// ListVisiblePaths applies .gitignore and repository exclude rules beneath directory.
// Contains accepts absolute paths. Tracked files remain visible even if an ignore rule matches.
// Global excludes are not read because git commands in this package isolate global configuration.
func ListVisiblePaths(ctx context.Context, directory string) (VisiblePaths, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return VisiblePaths{}, fmt.Errorf("resolve Git visibility directory %q: %w", directory, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return VisiblePaths{}, fmt.Errorf("canonicalize Git visibility directory %q: %w", absolute, err)
	}
	_, err = run(ctx, command{dir: canonical, args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		if strings.Contains(err.Error(), ErrNotRepository.Error()) {
			return VisiblePaths{directory: canonical}, nil
		}
		return VisiblePaths{}, fmt.Errorf("find Git repository for %q: %w", canonical, err)
	}
	listed, err := run(ctx, command{dir: canonical,
		args: []string{"--no-optional-locks", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "."}})
	if err != nil {
		return VisiblePaths{}, fmt.Errorf("list Git-visible paths under %q: %w", canonical, err)
	}
	visible := VisiblePaths{directory: canonical, paths: make(map[string]bool), repository: true}
	for _, item := range strings.Split(listed, "\x00") {
		if item == "" {
			continue
		}
		path := filepath.FromSlash(item)
		if !filepath.IsLocal(path) {
			return VisiblePaths{}, fmt.Errorf("git listed non-local path %q under %q", path, canonical)
		}
		for path != "." {
			visible.paths[filepath.Join(canonical, path)] = true
			path = filepath.Dir(path)
		}
	}
	return visible, nil
}

// Contains reports whether an absolute path is tracked or unignored in this checkout.
func (visible VisiblePaths) Contains(path string) bool {
	return !visible.repository || visible.paths[path]
}

// IsIgnored checks a path against the checkout's ignore rules, including a nested Git directory
// that ls-files in the parent checkout may omit.
func (visible VisiblePaths) IsIgnored(ctx context.Context, path string) (bool, error) {
	if !visible.repository {
		return false, nil
	}
	relative, err := filepath.Rel(visible.directory, path)
	if err != nil || !filepath.IsLocal(relative) {
		return false, fmt.Errorf("path %q is outside Git visibility directory %q", path, visible.directory)
	}
	_, err = run(ctx, command{dir: visible.directory,
		args: []string{"--no-optional-locks", "check-ignore", "-q", "--", relative}})
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check Git ignore rule for %q: %w", path, err)
}
