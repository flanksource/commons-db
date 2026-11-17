package git

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkDir visits tracked and unignored paths, applying each nested repository's rules.
// Outside a repository it visits all paths. Git metadata is never visited.
func WalkDir(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	visible, err := ListVisiblePaths(ctx, canonical)
	if err != nil {
		return err
	}
	views := map[string]VisiblePaths{canonical: visible}
	return filepath.WalkDir(canonical, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fn(path, entry, walkErr)
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == canonical {
			return fn(path, entry, nil)
		}
		view := views[filepath.Dir(path)]
		if !entry.IsDir() {
			if !view.Contains(path) {
				return nil
			}
			return fn(path, entry, nil)
		}
		_, markerErr := os.Lstat(filepath.Join(path, ".git"))
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return markerErr
		}
		if !view.Contains(path) {
			if markerErr != nil {
				return filepath.SkipDir
			}
			ignored, err := view.IsIgnored(ctx, path)
			if err != nil {
				return err
			}
			if ignored {
				return filepath.SkipDir
			}
		}
		if markerErr == nil {
			view, err = ListVisiblePaths(ctx, path)
			if err != nil {
				return err
			}
		}
		views[path] = view
		return fn(path, entry, nil)
	})
}
