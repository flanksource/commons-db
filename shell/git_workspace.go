package shell

import (
	"bytes"
	"errors"
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

	wt := checkout.Worktree
	if checkout.Since != "" || (wt.IsEnabled() && wt.Uncommitted) {
		files, err := dirtyFiles(ctx, source, checkout.Since)
		if err != nil {
			return "", nil, nil, err
		}
		extra["dirtyFiles"] = files
	}

	if !wt.IsEnabled() {
		return source, extra, nil, nil
	}

	worktree, cleanup, err := addNativeWorktree(ctx, source, baseDir, checkout)
	if err != nil {
		return "", nil, nil, err
	}
	if err := populateWorktree(ctx, source, worktree, wt); err != nil {
		_ = cleanup()
		return "", nil, nil, err
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

// populateWorktree carries content the freshly-created worktree does not have:
// uncommitted work (staged + unstaged + untracked) and gitignored content.
// Both are read-only against the source — nothing is ever stashed there.
func populateWorktree(ctx context.Context, source, target string, wt *connection.GitWorktree) error {
	if wt.Uncommitted {
		patch, err := gitOutput(ctx, source, "diff", "--cached", "--binary")
		if err != nil {
			return fmt.Errorf("read staged patch: %w", err)
		}
		if len(bytes.TrimSpace(patch)) > 0 {
			if err := gitApply(ctx, target, patch, "apply", "--index", "--whitespace=nowarn"); err != nil {
				return fmt.Errorf("apply staged patch: %w", err)
			}
		}

		if patch, err = gitOutput(ctx, source, "diff", "--binary"); err != nil {
			return fmt.Errorf("read unstaged patch: %w", err)
		}
		if len(bytes.TrimSpace(patch)) > 0 {
			if err := gitApply(ctx, target, patch, "apply", "--whitespace=nowarn"); err != nil {
				return fmt.Errorf("apply unstaged patch: %w", err)
			}
		}

		if err := copyListedFiles(ctx, source, target, "untracked",
			"ls-files", "--others", "--exclude-standard", "-z"); err != nil {
			return err
		}
	}

	if wt.Ignored {
		// --others --ignored --exclude-standard is the exact complement of the
		// untracked listing above: `git worktree add` brings neither.
		if err := copyListedFiles(ctx, source, target, "ignored",
			"ls-files", "--others", "--ignored", "--exclude-standard", "-z"); err != nil {
			return err
		}
	}

	return nil
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

// copyListedFiles copies every path emitted by a NUL-separated `git ls-files`
// invocation from source into target.
func copyListedFiles(ctx context.Context, source, target, kind string, args ...string) error {
	out, err := gitOutput(ctx, source, args...)
	if err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}

	var copied, cloned, bytesCopied int64
	for _, name := range strings.Split(string(out), "\x00") {
		name = strings.TrimSpace(name)
		if name == "" || name == ".git" || strings.HasPrefix(name, ".git/") {
			continue
		}
		src := filepath.Join(source, name)
		dst := filepath.Join(target, name)
		n, cow, err := copyPath(src, dst)
		if err != nil {
			return fmt.Errorf("copy %s %s: %w", kind, name, err)
		}
		copied++
		bytesCopied += n
		if cow {
			cloned++
		}
	}

	if copied > 0 {
		// A byte copy of a large ignored tree (node_modules) is the difference
		// between a fixture that starts instantly and one that doesn't, so make
		// the fallback visible rather than mysterious.
		ctx.Logger.V(3).Infof("copied %d %s files (%d bytes) into %s, %d via copy-on-write",
			copied, kind, bytesCopied, target, cloned)
	}
	return nil
}

// copyPath copies a single path, preferring a copy-on-write clone
// (clonefile(2) on APFS, FICLONE on btrfs/XFS) over a byte copy. It reports the
// bytes involved and whether the clone path was taken.
//
// It deliberately never hardlinks: a hardlinked file edited inside the worktree
// would corrupt the source repo.
func copyPath(src, dst string) (int64, bool, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return 0, false, err
		}
		_ = os.RemoveAll(dst)
		return 0, false, os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return 0, false, nil
	}

	if err := cloneFile(src, dst); err == nil {
		return info.Size(), true, nil
	} else if !errors.Is(err, errCloneUnsupported) {
		return 0, false, err
	}

	in, err := os.Open(src)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, false, err
	}
	defer out.Close()
	n, err := io.Copy(out, in)
	return n, false, err
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
