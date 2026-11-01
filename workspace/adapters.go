package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons/utils"
	"github.com/google/uuid"
)

var worktreeLocks = utils.NamedLock{}

type headAdapter struct{}

func (headAdapter) Prepare(ctx context.Context, source string, opts Options) (*Workspace, error) {
	if opts.Ignored || opts.Existing || opts.Keep || opts.Branch != "" || opts.Base != "" || opts.Path != "" || opts.Prefix != "" {
		return nil, errors.New("workspace: a head workspace is the source checkout itself; worktree options do not apply")
	}
	return describe(ctx, &Workspace{Kind: KindHead, Source: source, Dir: source})
}

type worktreeAdapter struct{ kind Kind }

func (a worktreeAdapter) Prepare(ctx context.Context, source string, opts Options) (*Workspace, error) {
	ws, err := addWorktree(ctx, source, opts)
	if err != nil {
		return nil, err
	}
	ws.Kind = a.kind
	if err := a.populate(ctx, ws, opts); err != nil {
		return nil, errors.Join(err, ws.Release())
	}
	if _, err := describe(ctx, ws); err != nil {
		return nil, errors.Join(err, ws.Release())
	}
	if err := ws.saveManifest(ctx); err != nil {
		return nil, errors.Join(err, ws.Release())
	}
	return ws, nil
}

func (a worktreeAdapter) populate(ctx context.Context, ws *Workspace, opts Options) (err error) {
	if a.kind == KindDirty {
		if ws.Carried, err = carryUncommitted(ctx, ws.Source, ws.Dir); err != nil {
			return err
		}
	}
	if opts.Ignored {
		ws.Ignored, err = cloneIgnored(ctx, ws.Source, ws.Dir)
	}
	return err
}

// describe fills in the branch and commit Dir has checked out; both are empty
// where they do not exist (an unborn branch has no commit, a detached HEAD no
// branch).
func describe(ctx context.Context, ws *Workspace) (*Workspace, error) {
	base, err := headCommit(ctx, ws.Dir)
	if err != nil {
		return nil, err
	}
	branch, err := headBranch(ctx, ws.Dir)
	if err != nil {
		return nil, err
	}
	ws.Base, ws.Branch = base, branch
	return ws, nil
}

func addWorktree(ctx context.Context, source string, opts Options) (*Workspace, error) {
	branch := strings.TrimSpace(opts.Branch)
	if opts.Existing && branch == "" {
		return nil, errors.New("workspace: an existing-branch worktree requires a branch")
	}
	if branch == "" {
		prefix := strings.Trim(strings.TrimSpace(opts.Prefix), "/")
		if prefix == "" {
			prefix = "shell"
		}
		branch = prefix + "/" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	target, err := worktreePath(opts, branch)
	if err != nil {
		return nil, err
	}

	args := []string{"worktree", "add", "-b", branch, target}
	if opts.Existing {
		args = []string{"worktree", "add", target, branch}
	} else if base := strings.TrimSpace(opts.Base); base != "" {
		args = append(args, base)
	}

	lock := worktreeLocks.TryLock(source, 5*time.Minute)
	if lock == nil {
		return nil, fmt.Errorf("workspace: failed to acquire worktree lock for %s", source)
	}
	defer lock.Release()

	if opts.Existing {
		if err := requireBranch(ctx, source, branch); err != nil {
			return nil, err
		}
	}
	if _, err := gitOutput(ctx, source, args...); err != nil {
		return nil, err
	}
	return &Workspace{Source: source, Dir: target, release: func() error {
		if opts.Keep {
			return nil
		}
		if _, err := gitOutput(ctx, source, "worktree", "remove", "--force", target); err != nil {
			return err
		}
		_, _ = gitOutput(ctx, source, "worktree", "prune")
		return nil
	}}, nil
}

func worktreePath(opts Options, branch string) (string, error) {
	target := strings.TrimSpace(opts.Path)
	if target == "" {
		if opts.BaseDir == "" {
			return "", errors.New("workspace: a worktree needs BaseDir or Path")
		}
		target = filepath.Join(opts.BaseDir, "worktrees", fmt.Sprintf("%s-%s", sanitizeWorktreeName(branch), uuid.NewString()))
	} else if !filepath.IsAbs(target) {
		if opts.BaseDir == "" {
			return "", fmt.Errorf("workspace: relative worktree path %q needs BaseDir", target)
		}
		target = filepath.Join(opts.BaseDir, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	// git reports the resolved path (/private/var, not /var), and Dir must match
	// what Open later resolves from inside the worktree.
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(target)), nil
}

// requireBranch accepts only an existing branch name. check-ref-format rejects
// revision expressions (main~1, main^) and show-ref matches only the exact ref,
// so the worktree always attaches HEAD to the named branch rather than
// detaching at a resolved commit no branch retains.
func requireBranch(ctx context.Context, repo, branch string) error {
	if name, err := gitString(ctx, repo, "check-ref-format", "--branch", branch); err != nil || name != branch || branch == "HEAD" {
		return fmt.Errorf("workspace: worktree branch %q is not a valid branch name", branch)
	}
	if _, err := gitOutput(ctx, repo, "show-ref", "--quiet", "--verify", "--", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("workspace: worktree branch %s does not exist in %s", branch, repo)
	}
	return nil
}

func sanitizeWorktreeName(name string) string {
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-").Replace(name)
}
