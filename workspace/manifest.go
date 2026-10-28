package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const manifestFile = "workspace.json"

// manifest is what a worktree workspace records about itself in its own git
// directory, so a later process — a commit hook, `gavel commit`, a push —
// opening the directory sees the same excludes the setup recorded.
type manifest struct {
	Kind    Kind     `json:"kind"`
	Source  string   `json:"source"`
	Base    string   `json:"base"`
	Carried []string `json:"carried,omitempty"`
	Ignored []string `json:"ignored,omitempty"`
}

// manifestPath is inside the checkout's git directory: per-worktree for a
// linked worktree (.git/worktrees/<name>), so it is never committed and is
// removed with the worktree.
func manifestPath(ctx context.Context, dir string) (string, error) {
	gitDir, err := gitString(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, manifestFile), nil
}

func (w *Workspace) saveManifest(ctx context.Context) error {
	path, err := manifestPath(ctx, w.Dir)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest{Kind: w.Kind, Source: w.Source, Base: w.Base, Carried: w.Carried, Ignored: w.Ignored}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("workspace: write manifest %s: %w", path, err)
	}
	return nil
}

// Open returns the workspace dir belongs to. A worktree Prepare created is
// reopened from its manifest with the excludes recorded then; any other
// checkout is a head workspace with none. The returned workspace does not own
// the directory: Release on it is a no-op.
func Open(ctx context.Context, dir string) (*Workspace, error) {
	root, err := checkoutRoot(ctx, dir)
	if err != nil {
		return nil, err
	}
	path, err := manifestPath(ctx, root)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return describe(ctx, &Workspace{Kind: KindHead, Source: root, Dir: root})
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("workspace: read manifest %s: %w", path, err)
	}
	if _, ok := adapters[m.Kind]; !ok {
		return nil, fmt.Errorf("workspace: manifest %s has unknown kind %q", path, m.Kind)
	}
	ws, err := describe(ctx, &Workspace{Kind: m.Kind, Source: m.Source, Dir: root, Carried: m.Carried, Ignored: m.Ignored})
	if err != nil {
		return nil, err
	}
	ws.Base = m.Base
	return ws, nil
}

// IsExcluded reports whether path is content the workspace cloned from the
// source's gitignored files, which no commit, snapshot or push may carry.
func (w *Workspace) IsExcluded(path string) bool {
	for _, entry := range w.Ignored {
		if dir, ok := strings.CutSuffix(entry, "/"); ok {
			if path == dir || strings.HasPrefix(path, entry) {
				return true
			}
		} else if path == entry {
			return true
		}
	}
	return false
}

func (w *Workspace) withoutExcluded(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if !w.IsExcluded(path) {
			out = append(out, path)
		}
	}
	return out
}

// Changes lists every path with uncommitted work — staged, unstaged or
// untracked — except gitignored and excluded content.
func (w *Workspace) Changes(ctx context.Context) ([]string, error) {
	paths, err := changedPaths(ctx, w.Dir)
	if err != nil {
		return nil, err
	}
	paths = w.withoutExcluded(paths)
	sort.Strings(paths)
	return paths, nil
}

func checkoutRoot(ctx context.Context, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := gitString(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("workspace: %s is not inside a git checkout: %w", dir, err)
	}
	return root, nil
}
