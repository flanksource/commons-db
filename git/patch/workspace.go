package patch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/git/internal/command"
)

type workspace struct{ dir string }

func open(ctx context.Context, directory string) (*workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if directory == "" {
		return nil, errors.New("patch workspace directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(directory, "patch-")
	if err != nil {
		return nil, err
	}
	w := &workspace{dir: dir}
	if _, err := w.git(ctx, nil, "init", "--quiet"); err != nil {
		return nil, errors.Join(err, w.close())
	}
	return w, nil
}

func (w *workspace) close() error { return os.RemoveAll(w.dir) }

func (w *workspace) git(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	result, err := command.Run(ctx, command.Options{Directory: w.dir, Input: input, Args: args,
		Environment: command.Environment(command.Config{Key: "core.autocrlf", Value: "false"}, command.Config{Key: "core.safecrlf", Value: "false"}, command.Config{Key: "merge.conflictStyle", Value: "diff3"})})
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr))
	}
	return result.Output, nil
}

func validPath(name string) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00\r\n") {
		return fmt.Errorf("invalid patch path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("invalid patch path %q", name)
		}
	}
	return nil
}

func (w *workspace) stage(ctx context.Context, files map[string][]byte) error {
	if _, err := w.git(ctx, nil, "read-tree", "--empty"); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var index strings.Builder
	for _, name := range names {
		if err := validPath(name); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := filepath.Join(w.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(abs, files[name], 0o600); err != nil {
			return err
		}
		object, err := w.git(ctx, files[name], "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return err
		}
		fmt.Fprintf(&index, "100644 %s\t%s\x00", strings.TrimSpace(string(object)), name)
	}
	_, err := w.git(ctx, []byte(index.String()), "update-index", "-z", "--index-info")
	if err != nil {
		return err
	}
	_, err = w.git(ctx, nil, "update-index", "--refresh")
	return err
}

func (w *workspace) clear(files map[string][]byte) error {
	for name := range files {
		if err := validPath(name); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(w.dir, filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	return nil
}

func (w *workspace) files(ctx context.Context) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(w.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("patch produced non-regular file %q", path)
		}
		name, err := filepath.Rel(w.dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(name)], err = os.ReadFile(path)
		return err
	})
	return files, err
}
