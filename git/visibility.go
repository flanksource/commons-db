package git

import (
	"context"
	"errors"
	"fmt"
	gitcommand "github.com/flanksource/commons-db/git/internal/command"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

type VisibilityPath struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type VisibilityOptions struct {
	Directory         string
	Paths             []VisibilityPath
	Overrides         []string
	OverrideDirectory string
	// IgnoreIndex matches tracked files against the ignore rules too
	// (`check-ignore --no-index`), so a force-tracked ignored file is reported.
	IgnoreIndex bool
}

type PathVisibility struct {
	Path    string `json:"path"`
	Visible bool   `json:"visible"`
	Rule    string `json:"rule,omitempty"`
	// BeyondSymlink is the symlinked directory (relative to Directory) the path
	// lies under. Git refuses such a path, so no native rule is applied to it.
	BeyondSymlink string `json:"beyondSymlink,omitempty"`
}

// CheckVisibility checks only supplied paths. Overrides use Git-style patterns,
// relative to Directory, in order after the checkout's native ignore rules.
// A path under a symlinked directory (a tracked file whose directory became a
// symlink) is visible: Git neither ignores nor traverses it. Outside a Git
// checkout only the overrides apply.
func CheckVisibility(ctx context.Context, options VisibilityOptions) ([]PathVisibility, error) {
	result := make([]PathVisibility, len(options.Paths))
	var checkedIndexes []int
	input := ""
	for index, path := range options.Paths {
		if !fs.ValidPath(path.Path) || path.Path == "." || strings.ContainsRune(path.Path, '\x00') {
			return nil, fmt.Errorf("invalid visibility path %q", path.Path)
		}
		link, err := SymlinkedParent(options.Directory, path.Path)
		if err != nil {
			return nil, err
		}
		result[index] = PathVisibility{Path: path.Path, Visible: true, BeyondSymlink: link}
		if link != "" {
			continue
		}
		checkedIndexes = append(checkedIndexes, index)
		input += path.Path
		if path.Directory {
			input += "/"
		}
		input += "\x00"
	}
	if len(checkedIndexes) > 0 {
		if err := applyNativeVisibility(ctx, options, input, checkedIndexes, result); err != nil {
			return nil, err
		}
	}
	for index, path := range options.Paths {
		overridePath := path.Path
		if options.OverrideDirectory != "" && options.OverrideDirectory != "." {
			if !fs.ValidPath(options.OverrideDirectory) {
				return nil, fmt.Errorf("invalid override directory")
			}
			var inside bool
			overridePath, inside = strings.CutPrefix(path.Path, options.OverrideDirectory+"/")
			if !inside {
				continue
			}
		}
		for _, override := range options.Overrides {
			if strings.ContainsAny(override, "\x00\r\n") {
				return nil, fmt.Errorf("ignore overrides must contain one pattern per entry")
			}
			match := gitignore.ParsePattern(override, nil).Match(strings.Split(overridePath, "/"), path.Directory)
			if match == gitignore.NoMatch {
				continue
			}
			result[index].Visible = match == gitignore.Include
			result[index].Rule = override
		}
	}
	return result, nil
}

// applyNativeVisibility runs `git check-ignore` over input (the NUL-separated
// paths of result[indexes]) and records each path's native rule.
func applyNativeVisibility(ctx context.Context, options VisibilityOptions, input string, indexes []int, result []PathVisibility) error {
	env, err := visibilityEnvironment(ctx, options.Directory)
	if err != nil {
		return err
	}
	args := []string{"check-ignore", "--stdin", "-z", "-v", "--non-matching"}
	if options.IgnoreIndex {
		args = append(args, "--no-index")
	}
	checked, err := gitcommand.Run(ctx, gitcommand.Options{Directory: options.Directory, Environment: env, Input: []byte(input), Args: args})
	var exit *exec.ExitError
	if err != nil && errors.As(err, &exit) && exit.ExitCode() == 128 && strings.Contains(checked.Stderr, ErrNotRepository.Error()) {
		// Outside a checkout every path is visible, as in ListVisiblePaths.
		return nil
	}
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return fmt.Errorf("check Git visibility: %w: %s", err, checked.Stderr)
	}
	fields := strings.Split(string(checked.Output), "\x00")
	if len(fields) != len(indexes)*4+1 {
		return fmt.Errorf("unexpected Git ignore response for %d paths", len(indexes))
	}
	for position, index := range indexes {
		rule := fields[position*4+2]
		if strings.TrimSuffix(fields[position*4+3], "/") != result[index].Path {
			return fmt.Errorf("git check-ignore returned an unexpected path")
		}
		result[index].Rule = rule
		result[index].Visible = rule == "" || strings.HasPrefix(rule, "!")
	}
	return nil
}

// SymlinkedParent returns the first directory of the slash-separated path rel,
// relative to root, that is a symlink, or "" when none is. Git refuses any
// pathspec that traverses one ("beyond a symbolic link"). Directories that no
// longer exist cannot be symlinks, so the walk stops at the first missing one.
func SymlinkedParent(root, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	for index := range parts[:len(parts)-1] {
		dir := strings.Join(parts[:index+1], "/")
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir)))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect %q under %q: %w", dir, root, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return dir, nil
		}
	}
	return "", nil
}
