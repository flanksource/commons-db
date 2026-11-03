package patch

import (
	"context"
	"errors"
	"strings"
)

type CreateOptions struct {
	Directory     string
	Before, After map[string][]byte
}

func Create(ctx context.Context, options CreateOptions) (patch string, err error) {
	w, err := open(ctx, options.Directory)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, w.close()) }()
	if err := w.stage(ctx, options.Before); err != nil {
		return "", err
	}
	tree, err := w.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	if err := w.clear(options.Before); err != nil {
		return "", err
	}
	if err := w.stage(ctx, options.After); err != nil {
		return "", err
	}
	diff, err := w.git(ctx, nil, "diff", "--cached", "--binary", "--full-index", "--no-renames", strings.TrimSpace(string(tree)), "--")
	return string(diff), err
}

type MergeOptions struct {
	Directory     string
	Base, Current map[string][]byte
	Patch         string
}

type Result struct {
	Files     map[string][]byte
	Conflicts []string
}

func Merge(ctx context.Context, options MergeOptions) (result Result, err error) {
	w, err := open(ctx, options.Directory)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, w.close()) }()
	if err := w.stage(ctx, options.Base); err != nil {
		return result, err
	}
	if err := w.clear(options.Base); err != nil {
		return result, err
	}
	if err := w.stage(ctx, options.Current); err != nil {
		return result, err
	}
	_, applyErr := w.git(ctx, []byte(options.Patch), "apply", "--3way", "--index", "--allow-empty", "-")
	conflicted, err := w.git(ctx, nil, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return result, err
	}
	if applyErr != nil && len(conflicted) == 0 {
		return result, applyErr
	}
	if len(conflicted) > 0 {
		result.Conflicts = strings.Split(strings.TrimSuffix(string(conflicted), "\x00"), "\x00")
	}
	result.Files, err = w.files(ctx)
	return result, err
}
