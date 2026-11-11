package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
)

type WorkingBlameOptions struct {
	Directory string
	Path      string
	Contents  []byte
}
type WorkingBlame struct {
	Lines       []BlameLine `json:"lines"`
	Unavailable string      `json:"unavailable,omitempty"`
}

// BlameWorkingFile returns provenance independently of loading working-tree text.
// Unborn and untracked files have a meaningful unavailable state, not a read error.
func BlameWorkingFile(ctx context.Context, options WorkingBlameOptions) (WorkingBlame, error) {
	if !fs.ValidPath(options.Path) || options.Path == "." || options.Contents == nil {
		return WorkingBlame{}, fmt.Errorf("working blame requires a relative path and displayed contents")
	}
	found, err := Discover(ctx, DiscoverOptions{Path: options.Directory})
	if err != nil {
		return WorkingBlame{}, err
	}
	if found.Bare {
		return WorkingBlame{}, fmt.Errorf("working blame requires a checkout")
	}
	head, err := run(ctx, command{dir: found.Root, args: []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}})
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return WorkingBlame{Lines: []BlameLine{}, Unavailable: "Repository has no commits"}, nil
	}
	if err != nil {
		return WorkingBlame{}, err
	}
	exists, err := run(ctx, command{dir: found.Root, args: []string{"--literal-pathspecs", "ls-tree", "--name-only", head, "--", options.Path}})
	if err != nil {
		return WorkingBlame{}, err
	}
	if exists == "" {
		return WorkingBlame{Lines: []BlameLine{}, Unavailable: "File is not present in HEAD"}, nil
	}
	raw, err := runBytes(ctx, command{dir: found.Root, stdin: options.Contents, args: []string{"blame", "--line-porcelain", "--root", "--contents", "-", "--", options.Path}})
	if err != nil {
		return WorkingBlame{}, err
	}
	lines, err := parseBlame(raw)
	return WorkingBlame{Lines: lines}, err
}
