package shell

import (
	"bytes"
	"fmt"
	osExec "os/exec"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/workspace"
)

// prepareCheckout turns a git checkout into the workspace the command runs in:
// a remote clone or a local checkout used in place, or a worktree of either.
func prepareCheckout(ctx context.Context, baseDir string, checkout *connection.GitConnection) (*workspace.Workspace, map[string]any, error) {
	if checkout.Path != "" && checkout.URL != "" {
		return nil, nil, fmt.Errorf("checkout.path and checkout.url are mutually exclusive")
	}

	if checkout.Path != "" {
		return prepareLocalCheckout(ctx, baseDir, checkout)
	}

	mountPoint, extra, err := prepareRemoteCheckout(ctx, baseDir, checkout)
	if err != nil {
		return nil, nil, err
	}
	opts := workspace.Options{Source: mountPoint, Kind: workspace.KindHead}
	if wt := checkout.Worktree; wt.IsEnabled() {
		// A fresh clone has neither uncommitted work nor ignored content to carry.
		opts = worktreeOptions(mountPoint, baseDir, checkout, workspace.KindClean)
		opts.Ignored = false
	}
	ws, err := workspace.Prepare(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if ws.Kind != workspace.KindHead {
		extra["worktree"] = ws.Dir
	}
	return ws, extra, nil
}

func prepareLocalCheckout(ctx context.Context, baseDir string, checkout *connection.GitConnection) (*workspace.Workspace, map[string]any, error) {
	wt := checkout.Worktree
	opts := workspace.Options{Source: checkout.Path, Kind: workspace.KindHead}
	if wt.IsEnabled() {
		kind := workspace.KindClean
		if wt.Uncommitted {
			kind = workspace.KindDirty
		}
		opts = worktreeOptions(checkout.Path, baseDir, checkout, kind)
	}
	ws, err := workspace.Prepare(ctx, opts)
	if err != nil {
		return nil, nil, err
	}

	extra := map[string]any{"git": "local", "path": ws.Source, "commit": ws.Base}
	if ws.Kind != workspace.KindHead {
		extra["worktree"] = ws.Dir
	}
	if checkout.Since != "" || ws.Kind == workspace.KindDirty {
		files, err := dirtyFiles(ctx, ws.Source, checkout.Since)
		if err != nil {
			return nil, nil, cleanupSetupFailure(err, ws.Release)
		}
		extra["dirtyFiles"] = files
	}
	return ws, extra, nil
}

func worktreeOptions(source, baseDir string, checkout *connection.GitConnection, kind workspace.Kind) workspace.Options {
	wt := checkout.Worktree
	base := strings.TrimSpace(wt.Base)
	if base == "" {
		base = checkout.Branch
	}
	return workspace.Options{
		Source: source, Kind: kind, BaseDir: baseDir, Path: wt.Path,
		Branch: wt.Branch, Prefix: wt.Prefix, Existing: wt.Existing, Base: base,
		Ignored: wt.Ignored, Keep: wt.Keep,
	}
}

func dirtyFiles(ctx context.Context, source, since string) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(files []string) {
		for _, file := range files {
			file = strings.TrimSpace(file)
			if file != "" {
				seen[file] = struct{}{}
			}
		}
	}

	if since != "" {
		base, err := gitString(ctx, source, "merge-base", "HEAD", since)
		if err != nil {
			return nil, fmt.Errorf("git merge-base HEAD %s: %w", since, err)
		}
		files, err := gitLines(ctx, source, "diff", "--name-only", base+"...HEAD")
		if err != nil {
			return nil, fmt.Errorf("git diff %s...HEAD: %w", base, err)
		}
		add(files)
	}

	for _, args := range [][]string{
		{"diff", "--cached", "--name-only"},
		{"diff", "--name-only"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		files, err := gitLines(ctx, source, args...)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		add(files)
	}

	out := make([]string, 0, len(seen))
	for file := range seen {
		out = append(out, file)
	}
	sort.Strings(out)
	return out, nil
}

func gitString(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutput(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitLines(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := gitOutput(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := osExec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func gitApply(ctx context.Context, dir string, patch []byte, args ...string) error {
	cmd := osExec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
