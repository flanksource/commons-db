package shell

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons/merge"
)

// Resolve returns an independent setup whose filesystem paths are absolute and
// anchored to baseDir. This makes a serialized setup safe to execute from a
// long-lived server whose process directory may differ from the caller's
// workspace.
//
// Independence comes from merge.Clone, structurally — every field, including
// ones added after this was written. Anchoring the paths is the only thing left
// for this function to do.
func (s Setup) Resolve(baseDir string) (Setup, error) {
	baseDir, err := filepath.Abs(strings.TrimSpace(baseDir))
	if err != nil {
		return Setup{}, fmt.Errorf("resolve setup base directory %q: %w", baseDir, err)
	}
	baseDir = filepath.Clean(baseDir)

	resolved := merge.Clone(s, merge.Policy{})
	resolved.Cwd = resolveSetupPath(baseDir, s.Cwd, baseDir)
	resolved.BaseDir = resolveSetupPath(baseDir, s.BaseDir, filepath.Join(baseDir, ".shell"))
	for i := range resolved.DotEnv {
		resolved.DotEnv[i] = resolveSetupPath(baseDir, resolved.DotEnv[i], "")
	}
	if checkout := resolved.Checkout; checkout != nil {
		checkout.Path = resolveSetupPath(baseDir, checkout.Path, "")
		// Only an existing worktree names a path the caller chose; the others are
		// created by Prepare, which decides where they land.
		if checkout.Worktree != nil && checkout.Worktree.Mode == WorktreeExisting {
			checkout.Worktree.Path = resolveSetupPath(baseDir, checkout.Worktree.Path, "")
		}
	}
	return resolved, nil
}

func resolveSetupPath(baseDir, path, fallback string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return fallback
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}
