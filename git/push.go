package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PushOptions configures Worktree.Push. Branch is a local branch pushed to
// RemoteBranch (default: the same name). A push never overwrites history
// unless Force is set, and then only with --force-with-lease against
// ExpectedSHA. ServerOptions are sent as git push options (-o).
type PushOptions struct {
	Branch        string
	RemoteBranch  string
	Tags          []string
	Force         bool
	ExpectedSHA   string
	ServerOptions []string
}

func (o PushOptions) remoteBranch() string {
	if o.RemoteBranch != "" {
		return o.RemoteBranch
	}
	return o.Branch
}

func (o PushOptions) refspecs() ([]string, error) {
	var refspecs []string
	if o.Branch != "" {
		for _, name := range []string{o.Branch, o.remoteBranch()} {
			if err := ValidateBranchName(name); err != nil {
				return nil, err
			}
		}
		refspecs = append(refspecs, "refs/heads/"+o.Branch+":refs/heads/"+o.remoteBranch())
	}
	for _, tag := range o.Tags {
		if err := ValidateTagName(tag); err != nil {
			return nil, err
		}
		refspecs = append(refspecs, "refs/tags/"+tag+":refs/tags/"+tag)
	}
	if len(refspecs) == 0 {
		return nil, errors.New("push needs a branch or a tag")
	}
	return refspecs, nil
}

func (o PushOptions) flags() ([]string, error) {
	var flags []string
	if o.Force {
		if o.Branch == "" || !fullSHAPattern.MatchString(o.ExpectedSHA) {
			return nil, errors.New("a forced push needs a branch and the full ExpectedSHA of the remote branch")
		}
		flags = append(flags, "--force-with-lease=refs/heads/"+o.remoteBranch()+":"+o.ExpectedSHA)
	}
	for _, option := range o.ServerOptions {
		if option == "" || strings.ContainsAny(option, "\n\x00") {
			return nil, fmt.Errorf("invalid push option %q", option)
		}
		flags = append(flags, "--push-option="+option)
	}
	return flags, nil
}

// Push pushes the branch and tags to the worktree's repository.
func (w *Worktree) Push(ctx context.Context, options PushOptions) error {
	refspecs, err := options.refspecs()
	if err != nil {
		return err
	}
	flags, err := options.flags()
	if err != nil {
		return err
	}
	env, cleanup, err := commandEnv(w.repo)
	if err != nil {
		return err
	}
	defer cleanup()
	args := append(append([]string{"push", "--quiet"}, flags...), "--end-of-options", w.repo.URL.Clone)
	if _, err := run(ctx, command{dir: w.Dir, env: env, args: append(args, refspecs...)}); err != nil {
		return fmt.Errorf("push to %s: %w", w.repo.URL.Redacted(), err)
	}
	return nil
}
