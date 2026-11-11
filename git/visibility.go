package git

import (
	"context"
	"errors"
	"fmt"
	gitcommand "github.com/flanksource/commons-db/git/internal/command"
	"io/fs"
	"os/exec"
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
}

type PathVisibility struct {
	Path    string `json:"path"`
	Visible bool   `json:"visible"`
	Rule    string `json:"rule,omitempty"`
}

// CheckVisibility checks only supplied paths. Overrides use Git-style patterns,
// relative to Directory, in order after the checkout's native ignore rules.
func CheckVisibility(ctx context.Context, options VisibilityOptions) ([]PathVisibility, error) {
	result := make([]PathVisibility, len(options.Paths))
	input := ""
	for index, path := range options.Paths {
		if !fs.ValidPath(path.Path) || path.Path == "." || strings.ContainsRune(path.Path, '\x00') {
			return nil, fmt.Errorf("invalid visibility path %q", path.Path)
		}
		result[index] = PathVisibility{Path: path.Path, Visible: true}
		input += path.Path
		if path.Directory {
			input += "/"
		}
		input += "\x00"
	}
	if len(result) == 0 {
		return result, nil
	}
	checked, err := gitcommand.Run(ctx, gitcommand.Options{Directory: options.Directory, Input: []byte(input), Args: []string{"check-ignore", "--stdin", "-z", "-v", "--non-matching"}})
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("check Git visibility: %w: %s", err, checked.Stderr)
	}
	fields := strings.Split(string(checked.Output), "\x00")
	if len(fields) != len(result)*4+1 {
		return nil, fmt.Errorf("unexpected Git ignore response for %d paths", len(result))
	}
	for index, path := range options.Paths {
		rule := fields[index*4+2]
		if strings.TrimSuffix(fields[index*4+3], "/") != path.Path {
			return nil, fmt.Errorf("Git ignore returned an unexpected path")
		}
		result[index].Rule = rule
		result[index].Visible = rule == "" || strings.HasPrefix(rule, "!")
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
