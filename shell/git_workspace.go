package shell

import (
	"bytes"
	"fmt"
	"io"
	"os"
	osExec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/google/uuid"
)

func prepareCheckout(ctx context.Context, baseDir string, checkout *connection.GitConnection) (string, map[string]any, func() error, error) {
	if checkout.Path != "" && checkout.URL != "" {
		return "", nil, nil, fmt.Errorf("checkout.path and checkout.url are mutually exclusive")
	}

	if checkout.Path != "" {
		return prepareLocalCheckout(ctx, baseDir, checkout)
	}

	mountPoint, extra, err := prepareRemoteCheckout(ctx, baseDir, checkout)
	if err != nil {
		return "", nil, nil, err
	}

	if checkout.Worktree != nil && checkout.Worktree.IsEnabled() {
		worktree, cleanup, err := addNativeWorktree(ctx, mountPoint, baseDir, checkout)
		if err != nil {
			return "", nil, nil, err
		}
		extra["worktree"] = worktree
		return worktree, extra, cleanup, nil
	}

	return mountPoint, extra, nil, nil
}

func prepareLocalCheckout(ctx context.Context, baseDir string, checkout *connection.GitConnection) (string, map[string]any, func() error, error) {
	source, err := filepath.Abs(checkout.Path)
	if err != nil {
		return "", nil, nil, err
	}
	source, err = gitRoot(ctx, source)
	if err != nil {
		return "", nil, nil, err
	}

	extra, err := gitMetadata(ctx, source)
	if err != nil {
		return "", nil, nil, err
	}
	extra["git"] = "local"
	extra["path"] = source

	if checkout.Dirty != nil && !checkout.Dirty.IsEmpty() {
		files, err := dirtyFiles(ctx, source, checkout.Dirty)
		if err != nil {
			return "", nil, nil, err
		}
		extra["dirtyFiles"] = files
	}

	if checkout.Worktree == nil || !checkout.Worktree.IsEnabled() {
		return source, extra, nil, nil
	}

	worktree, cleanup, err := addNativeWorktree(ctx, source, baseDir, checkout)
	if err != nil {
		return "", nil, nil, err
	}
	if checkout.Dirty != nil && !checkout.Dirty.IsEmpty() {
		if err := applyDirtyState(ctx, source, worktree, checkout.Dirty); err != nil {
			_ = cleanup()
			return "", nil, nil, err
		}
	}
	extra["worktree"] = worktree
	return worktree, extra, cleanup, nil
}

func addNativeWorktree(ctx context.Context, repo, baseDir string, checkout *connection.GitConnection) (string, func() error, error) {
	wt := checkout.Worktree
	if wt == nil {
		return "", nil, fmt.Errorf("checkout.worktree is required")
	}

	branch := strings.TrimSpace(wt.Branch)
	if branch == "" {
		prefix := strings.Trim(strings.TrimSpace(wt.Prefix), "/")
		if prefix == "" {
			prefix = "shell"
		}
		branch = prefix + "/" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}

	target := strings.TrimSpace(wt.Path)
	if target == "" {
		target = filepath.Join(baseDir, "worktrees", fmt.Sprintf("%s-%s", sanitizeWorktreeName(branch), uuid.NewString()))
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", nil, err
	}

	args := []string{"worktree", "add", "-b", branch, target}
	if base := strings.TrimSpace(wt.Base); base != "" {
		args = append(args, base)
	} else if checkout.Branch != "" {
		args = append(args, checkout.Branch)
	}

	lock := checkoutLocks.TryLock(repo, 5*time.Minute)
	if lock == nil {
		return "", nil, fmt.Errorf("failed to acquire checkout lock for %s", repo)
	}
	defer lock.Release()

	if _, err := gitOutput(ctx, repo, args...); err != nil {
		return "", nil, err
	}

	cleanup := func() error {
		if wt.Keep {
			return nil
		}
		if _, err := gitOutput(ctx, repo, "worktree", "remove", "--force", target); err != nil {
			return err
		}
		_, _ = gitOutput(ctx, repo, "worktree", "prune")
		return nil
	}
	return target, cleanup, nil
}

func applyDirtyState(ctx context.Context, source, target string, dirty *connection.GitDirty) error {
	if dirty == nil || dirty.IsEmpty() {
		return nil
	}

	if dirty.Staged {
		patch, err := gitOutput(ctx, source, "diff", "--cached", "--binary")
		if err != nil {
			return fmt.Errorf("read staged patch: %w", err)
		}
		if len(bytes.TrimSpace(patch)) > 0 {
			if err := gitApply(ctx, target, patch, "apply", "--index", "--whitespace=nowarn"); err != nil {
				return fmt.Errorf("apply staged patch: %w", err)
			}
		}
	}

	if dirty.Unstaged {
		args := []string{"diff", "--binary"}
		if !dirty.Staged {
			args = []string{"diff", "--binary", "HEAD"}
		}
		patch, err := gitOutput(ctx, source, args...)
		if err != nil {
			return fmt.Errorf("read unstaged patch: %w", err)
		}
		if len(bytes.TrimSpace(patch)) > 0 {
			if err := gitApply(ctx, target, patch, "apply", "--whitespace=nowarn"); err != nil {
				return fmt.Errorf("apply unstaged patch: %w", err)
			}
		}
	}

	if dirty.Untracked {
		if err := copyUntrackedFiles(ctx, source, target); err != nil {
			return err
		}
	}

	return nil
}

func dirtyFiles(ctx context.Context, source string, dirty *connection.GitDirty) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(files []string) {
		for _, file := range files {
			file = strings.TrimSpace(file)
			if file != "" {
				seen[file] = struct{}{}
			}
		}
	}

	if dirty.Since != "" {
		base, err := gitString(ctx, source, "merge-base", "HEAD", dirty.Since)
		if err != nil {
			return nil, fmt.Errorf("git merge-base HEAD %s: %w", dirty.Since, err)
		}
		files, err := gitLines(ctx, source, "diff", "--name-only", base+"...HEAD")
		if err != nil {
			return nil, fmt.Errorf("git diff %s...HEAD: %w", base, err)
		}
		add(files)
	}
	if dirty.Staged {
		files, err := gitLines(ctx, source, "diff", "--cached", "--name-only")
		if err != nil {
			return nil, fmt.Errorf("git diff --cached --name-only: %w", err)
		}
		add(files)
	}
	if dirty.Unstaged {
		files, err := gitLines(ctx, source, "diff", "--name-only")
		if err != nil {
			return nil, fmt.Errorf("git diff --name-only: %w", err)
		}
		add(files)
	}
	if dirty.Untracked {
		files, err := gitLines(ctx, source, "ls-files", "--others", "--exclude-standard")
		if err != nil {
			return nil, fmt.Errorf("git ls-files --others: %w", err)
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

func copyUntrackedFiles(ctx context.Context, source, target string) error {
	out, err := gitOutput(ctx, source, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("git ls-files --others: %w", err)
	}
	for _, name := range strings.Split(string(out), "\x00") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		src := filepath.Join(source, name)
		dst := filepath.Join(target, name)
		if err := copyPath(src, dst); err != nil {
			return fmt.Errorf("copy untracked %s: %w", name, err)
		}
	}
	return nil
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func gitRoot(ctx context.Context, dir string) (string, error) {
	return gitString(ctx, dir, "rev-parse", "--show-toplevel")
}

func gitMetadata(ctx context.Context, dir string) (map[string]any, error) {
	commit, err := gitString(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	return map[string]any{"commit": commit}, nil
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

func sanitizeWorktreeName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return replacer.Replace(name)
}
