package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// carryUncommitted replays the source's staged, unstaged and untracked work
// into target. The source is only read — nothing is ever stashed there.
func carryUncommitted(ctx context.Context, source, target string) ([]string, error) {
	for _, step := range []struct {
		name  string
		diff  []string
		apply []string
	}{
		{"staged", []string{"diff", "--cached", "--binary"}, []string{"apply", "--index", "--whitespace=nowarn"}},
		{"unstaged", []string{"diff", "--binary"}, []string{"apply", "--whitespace=nowarn"}},
	} {
		patch, err := gitOutput(ctx, source, step.diff...)
		if err != nil {
			return nil, fmt.Errorf("read %s patch: %w", step.name, err)
		}
		if len(bytes.TrimSpace(patch)) == 0 {
			continue
		}
		if _, err := (gitCmd{Dir: target, Stdin: patch}).output(ctx, step.apply...); err != nil {
			return nil, fmt.Errorf("apply %s patch: %w", step.name, err)
		}
	}
	if _, err := copyListedFiles(ctx, listedFilesCopy{
		Source: source, Target: target, Kind: "untracked",
		Args: []string{"ls-files", "--others", "--exclude-standard", "-z"},
	}); err != nil {
		return nil, err
	}
	return changedPaths(ctx, source)
}

// changedPaths lists every path the source holds uncommitted work for.
func changedPaths(ctx context.Context, source string) ([]string, error) {
	out, err := gitOutput(ctx, source, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	records := splitNUL(out)
	var paths []string
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			return nil, fmt.Errorf("git status in %s: malformed record %q", source, record)
		}
		paths = append(paths, record[3:])
		// A rename or copy is followed by its source path as a record of its own.
		if record[0] == 'R' || record[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

// cloneIgnored copies the source's gitignored content into target, skipping
// any path target tracks: the checked-out branch's committed content wins. It
// returns the copied content as compact exclude entries — a file, or a
// directory ("vendor/") wholly ignored by the source — so a node_modules tree
// is one entry rather than every file in it.
func cloneIgnored(ctx context.Context, source, target string) ([]string, error) {
	tracked, err := gitOutput(ctx, target, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files in %s: %w", target, err)
	}
	skip := map[string]struct{}{}
	for _, name := range splitNUL(tracked) {
		skip[name] = struct{}{}
	}
	copied, err := copyListedFiles(ctx, listedFilesCopy{
		Source: source, Target: target, Kind: "ignored", Skip: skip,
		Args: []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"},
	})
	if err != nil {
		return nil, err
	}
	entries, err := gitOutput(ctx, source, "ls-files", "--others", "--ignored", "--exclude-standard",
		"--directory", "--no-empty-directory", "-z")
	if err != nil {
		return nil, err
	}
	return compactExcludes(splitNUL(entries), copied, skip), nil
}

// compactExcludes keeps each directory entry whole unless target tracks a path
// inside it; such a directory is expanded into the files actually copied, so
// the branch's tracked file is never excluded from its own commits.
func compactExcludes(entries, copied []string, tracked map[string]struct{}) []string {
	var out []string
	for _, entry := range entries {
		if entry == ".git/" || strings.HasPrefix(entry, ".git/") {
			continue
		}
		if !strings.HasSuffix(entry, "/") {
			if _, ok := tracked[entry]; !ok {
				out = append(out, entry)
			}
			continue
		}
		if !tracksUnder(tracked, entry) {
			out = append(out, entry)
			continue
		}
		for _, path := range copied {
			if strings.HasPrefix(path, entry) {
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out
}

func tracksUnder(tracked map[string]struct{}, dir string) bool {
	for path := range tracked {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	return false
}

type listedFilesCopy struct {
	Source, Target, Kind string
	Skip                 map[string]struct{}
	Args                 []string
}

// copyListedFiles copies every path a NUL-separated `git ls-files` emits from
// source into target, and returns the paths it copied.
func copyListedFiles(ctx context.Context, opts listedFilesCopy) ([]string, error) {
	out, err := gitOutput(ctx, opts.Source, opts.Args...)
	if err != nil {
		return nil, err
	}

	var copied []string
	var cloned, bytesCopied int64
	for _, name := range splitNUL(out) {
		if name == ".git" || strings.HasPrefix(name, ".git/") {
			continue
		}
		if _, ok := opts.Skip[name]; ok {
			continue
		}
		n, cow, err := copyPath(filepath.Join(opts.Source, name), filepath.Join(opts.Target, name))
		if err != nil {
			return nil, fmt.Errorf("copy %s %s: %w", opts.Kind, name, err)
		}
		copied = append(copied, name)
		bytesCopied += n
		if cow {
			cloned++
		}
	}

	if len(copied) > 0 {
		// A byte copy of a large ignored tree (node_modules) is the difference
		// between a workspace that starts instantly and one that doesn't, so make
		// the fallback visible rather than mysterious.
		ctx.Logger.V(3).Infof("copied %d %s files (%d bytes) into %s, %d via copy-on-write",
			len(copied), opts.Kind, bytesCopied, opts.Target, cloned)
	}
	return copied, nil
}

// copyPath copies a single path, preferring a copy-on-write clone
// (clonefile(2) on APFS, FICLONE on btrfs/XFS) over a byte copy. It reports the
// bytes involved and whether the clone path was taken.
//
// It deliberately never hardlinks: a hardlinked file edited inside the
// workspace would corrupt the source repo.
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
