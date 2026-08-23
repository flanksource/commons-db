package shell

import (
	"errors"
	"fmt"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/hash"
	"github.com/flanksource/commons/properties"
	"github.com/flanksource/commons/utils"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

var checkoutLocks = utils.NamedLock{}

type SetupResult struct {
	Env             []string
	Cwd             string
	Extra           map[string]any
	Artifacts       []Artifact
	Cleanup         func() error
	sensitiveValues []string
}

type Setup struct {
	Cwd         string                     `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	BaseDir     string                     `json:"baseDir,omitempty" yaml:"baseDir,omitempty"`
	DotEnv      []string                   `json:"dotenv,omitempty" yaml:"dotenv,omitempty"`
	EnvVars     []types.EnvVar             `json:"envVars,omitempty" yaml:"envVars,omitempty"`
	Checkout    *Checkout                  `json:"checkout,omitempty" yaml:"checkout,omitempty"`
	Connections connection.ExecConnections `json:"connections,omitempty" yaml:"connections,omitempty"`
	Env         []string                   `json:"-" yaml:"-"`
}

type Checkout struct {
	Mode       CheckoutMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	URL        string       `json:"url,omitempty" yaml:"url,omitempty"`
	Path       string       `json:"path,omitempty" yaml:"path,omitempty"`
	Connection string       `json:"connection,omitempty" yaml:"connection,omitempty"`
	Ref        string       `json:"ref,omitempty" yaml:"ref,omitempty"`
	Depth      *int         `json:"depth,omitempty" yaml:"depth,omitempty"`
	// Since is a commit-ish. When set, the merge-base diff against HEAD is
	// folded into the reported `dirtyFiles`. Purely informational — it has no
	// bearing on what the worktree contains.
	Since string `json:"since,omitempty" yaml:"since,omitempty"`
	// Deprecated: no-op, retained so existing configs keep parsing. What a new
	// worktree contains is controlled by Worktree.Uncommitted and
	// Worktree.Ignored; dirtyFiles reporting is controlled by Since.
	Dirty    *Dirty    `json:"dirty,omitempty" yaml:"dirty,omitempty"`
	Worktree *Worktree `json:"worktree,omitempty" yaml:"worktree,omitempty"`
}

// Deprecated: no-op. See Checkout.Dirty.
type Dirty struct {
	Stash StashMode `json:"stash,omitempty" yaml:"stash,omitempty"`
	Since string    `json:"since,omitempty" yaml:"since,omitempty"`
}

// Deprecated: no-op. See Checkout.Dirty.
type StashMode string

// Deprecated: no-op. See Checkout.Dirty.
const (
	StashNone      StashMode = "none"
	StashUntracked StashMode = "untracked"
	StashUnstaged  StashMode = "unstaged"
	StashStaged    StashMode = "staged"
	StashAll       StashMode = "all"
)

type CheckoutMode string

const (
	CheckoutNone   CheckoutMode = "none"
	CheckoutLocal  CheckoutMode = "local"
	CheckoutRemote CheckoutMode = "remote"
)

type Worktree struct {
	Mode   WorktreeMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	Prefix string       `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Base   string       `json:"base,omitempty" yaml:"base,omitempty"`
	Path   string       `json:"path,omitempty" yaml:"path,omitempty"`
	Keep   bool         `json:"keep,omitempty" yaml:"keep,omitempty"`

	// Uncommitted controls whether staged, unstaged and untracked changes are
	// carried from the source repo into the new worktree. Nothing is ever
	// stashed — the source repo is never mutated.
	Uncommitted CloneMode `json:"uncommitted,omitempty" yaml:"uncommitted,omitempty"`
	// Ignored controls whether gitignored content (node_modules/, .env, build
	// caches) is copied into the new worktree. `git worktree add` never brings
	// it, so without this the worktree is missing everything .gitignore hides.
	Ignored CloneMode `json:"ignored,omitempty" yaml:"ignored,omitempty"`
}

// CloneMode says what a new worktree ends up containing. It describes the
// destination, not a mechanism applied to the source.
type CloneMode string

const (
	CloneClone CloneMode = "clone"
	CloneSkip  CloneMode = "skip"
)

func (c CloneMode) IsClone() bool { return c == CloneClone }

type WorktreeMode string

const (
	WorktreeNone     WorktreeMode = "none"
	WorktreeNew      WorktreeMode = "new"
	WorktreeExisting WorktreeMode = "existing"
)

// ApplyDefaults fills in the worktree defaults and returns any downgrade
// warnings the caller should surface. Callers opt in explicitly — Prepare does
// not call it, so an unset Worktree keeps git's own fallbacks.
//
// Defaults: base=HEAD, ignored=clone, uncommitted=clone when base is HEAD.
// Uncommitted work is a diff against *your* HEAD; replaying it onto a worktree
// branched elsewhere applies to the wrong context, so a non-HEAD base degrades
// uncommitted to skip unless it was set explicitly.
func (w *Worktree) ApplyDefaults() []string {
	if w == nil || w.Mode == "" || w.Mode == WorktreeNone {
		return nil
	}

	var warnings []string
	if strings.TrimSpace(w.Base) == "" {
		w.Base = "HEAD"
	}
	if w.Ignored == "" {
		w.Ignored = CloneClone
	}
	if w.Uncommitted == "" {
		if w.Base == "HEAD" {
			w.Uncommitted = CloneClone
		} else {
			w.Uncommitted = CloneSkip
			warnings = append(warnings, fmt.Sprintf(
				"worktree.base=%s is not HEAD; defaulting uncommitted: skip — set uncommitted: clone to force", w.Base))
		}
	}
	return warnings
}

func Prepare(ctx context.Context, setup *Setup) (*SetupResult, error) {
	if setup == nil {
		setup = &Setup{}
	}
	return SetupEnv(ctx, setup.ToExec())
}

func (s Setup) ToExec() *Exec {
	return &Exec{
		Connections: s.Connections,
		Checkout:    s.Checkout.toGitConnection(s.Cwd),
		EnvVars:     s.EnvVars,
		DotEnv:      s.DotEnv,
		Chroot:      s.Cwd,
		BaseDir:     s.BaseDir,
	}
}

func (c *Checkout) toGitConnection(cwd string) *connection.GitConnection {
	if c == nil {
		return nil
	}
	mode := c.Mode
	if mode == "" {
		switch {
		case c.URL != "":
			mode = CheckoutRemote
		case c.Path != "":
			mode = CheckoutLocal
		default:
			mode = CheckoutNone
		}
	}
	if mode == CheckoutNone {
		return nil
	}

	git := &connection.GitConnection{
		Connection: c.Connection,
		Branch:     c.Ref,
		Depth:      c.Depth,
		Since:      c.Since,
	}
	switch mode {
	case CheckoutRemote:
		git.URL = c.URL
	case CheckoutLocal:
		git.Path = c.Path
		if git.Path == "" {
			git.Path = cwd
		}
	}

	if c.Worktree != nil {
		if wt := c.Worktree.toGitWorktree(); wt != nil {
			if c.Worktree.Mode == WorktreeExisting {
				git.Path = c.Worktree.Path
				git.Worktree = nil
			} else {
				git.Worktree = wt
			}
		}
	}
	return git
}

func (w *Worktree) toGitWorktree() *connection.GitWorktree {
	if w == nil || w.Mode == "" || w.Mode == WorktreeNone {
		return nil
	}
	if w.Mode == WorktreeExisting {
		if strings.TrimSpace(w.Path) == "" {
			return nil
		}
		return &connection.GitWorktree{Path: w.Path}
	}
	return &connection.GitWorktree{
		Enabled:     true,
		Prefix:      w.Prefix,
		Base:        w.Base,
		Keep:        w.Keep,
		Uncommitted: w.Uncommitted.IsClone(),
		Ignored:     w.Ignored.IsClone(),
	}
}

func SetupEnv(ctx context.Context, exec *Exec) (*SetupResult, error) {
	if exec == nil {
		exec = &Exec{}
	}
	if err := normalizeBaseDir(exec); err != nil {
		return nil, err
	}

	envParams, workspaceCleanup, err := prepareEnvironment(ctx, exec)
	if err != nil {
		return nil, err
	}

	cwd := envParams.mountPoint
	if cwd == "" {
		cwd, err = defaultCommandDir(*exec)
		if err != nil {
			return nil, cleanupSetupFailure(err, workspaceCleanup)
		}
	}

	envs, sensitiveValues, err := buildEnv(*exec, envParams.envs)
	if err != nil {
		return nil, cleanupSetupFailure(err, workspaceCleanup)
	}
	sensitiveValues = append(sensitiveValues, envParams.sensitiveValues...)

	cmd := osExec.CommandContext(ctx, "true")
	cmd.Dir = cwd
	cmd.Env = envs

	setupResult, err := connection.SetupConnection(ctx, exec.Connections, cmd)
	if err != nil {
		return nil, ctx.Oops().Wrap(cleanupSetupFailure(err, workspaceCleanup))
	}
	sensitiveValues = append(sensitiveValues, connectionSensitiveValues(exec.Connections)...)

	cleanup := cleanupAll(workspaceCleanup, func() error {
		if waitBeforeCleanup := ctx.Properties().Duration("shell.connection.wait_before_cleanup", 0); waitBeforeCleanup > 0 {
			time.Sleep(waitBeforeCleanup)
		}
		if setupResult.Cleanup == nil {
			return nil
		}
		return setupResult.Cleanup()
	})

	sensitiveValues = append(sensitiveValues, addedEnvironmentValues(envs, cmd.Env)...)
	return &SetupResult{
		Env:             mergeEnvSlices(cmd.Env),
		Cwd:             cwd,
		Extra:           envParams.extra,
		Artifacts:       exec.Artifacts,
		Cleanup:         cleanup,
		sensitiveValues: uniqueValues(sensitiveValues),
	}, nil
}

func cleanupSetupFailure(setupErr error, cleanups ...func() error) error {
	if cleanupErr := cleanupAll(cleanups...)(); cleanupErr != nil {
		return errors.Join(setupErr, cleanupErr)
	}
	return setupErr
}

func normalizeBaseDir(exec *Exec) error {
	if exec.BaseDir == "" {
		exec.BaseDir = ".shell"
	}
	abs, err := filepath.Abs(exec.BaseDir)
	if err != nil {
		return fmt.Errorf("error getting absolute path for base directory: %w", err)
	}
	exec.BaseDir = abs
	return nil
}

func defaultCommandDir(exec Exec) (string, error) {
	if exec.Chroot != "" {
		chroot, err := filepath.Abs(exec.Chroot)
		if err != nil {
			return "", err
		}
		stat, err := os.Stat(chroot)
		if err != nil {
			return "", err
		}
		if !stat.IsDir() {
			return "", fmt.Errorf("%s is not a directory", chroot)
		}
		return chroot, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	base := properties.String(exec.BaseDir, "shell.tmp.dir")
	if base == "" {
		base = filepath.Join(cwd, ".shell")
	}
	cmdDir := filepath.Join(base, "tmp", uuid.New().String())
	if err := os.MkdirAll(cmdDir, 0700); err != nil {
		return "", err
	}
	return cmdDir, nil
}

func prepareEnvironment(ctx context.Context, exec *Exec) (*commandContext, func() error, error) {
	result := commandContext{
		extra: make(map[string]any),
	}

	for _, env := range exec.EnvVars {
		val, err := ctx.GetEnvValueFromCache(env, ctx.GetNamespace())
		if err != nil {
			return nil, nil, fmt.Errorf("error fetching env value (name=%s): %w", env.Name, err)
		}
		result.envs = append(result.envs, fmt.Sprintf("%s=%s", env.Name, val))
		result.sensitiveValues = append(result.sensitiveValues, val)
	}

	if exec.Checkout == nil {
		return &result, nil, nil
	}

	checkout := *exec.Checkout
	if err := checkout.HydrateConnection(ctx); err != nil {
		return nil, nil, fmt.Errorf("error hydrating connection: %w", err)
	}

	mountPoint, extra, cleanup, err := prepareCheckout(ctx, exec.BaseDir, &checkout)
	if err != nil {
		return nil, nil, err
	}
	result.mountPoint = mountPoint
	for k, v := range extra {
		result.extra[k] = v
	}
	return &result, cleanup, nil
}

func prepareRemoteCheckout(ctx context.Context, baseDir string, checkout *connection.GitConnection) (string, map[string]any, error) {
	if checkout.URL == "" {
		return "", nil, fmt.Errorf("checkout.url is required when checkout.path is empty")
	}

	var mountPoint string
	if dir := lo.FromPtr(checkout.Destination); dir != "" {
		if filepath.IsAbs(dir) {
			mountPoint = dir
		} else {
			mountPoint = filepath.Join(baseDir, dir)
		}
	} else {
		mountPoint = filepath.Join(baseDir, "checkout", hash.Sha256Hex(checkout.URL))
	}

	lock := checkoutLocks.TryLock(mountPoint, 5*time.Minute)
	if lock == nil {
		return "", nil, fmt.Errorf("failed to acquire checkout lock for %s", mountPoint)
	}
	defer lock.Release()

	client, err := connection.CreateGitConfig(ctx, checkout)
	if err != nil {
		return "", nil, err
	}

	extra, err := client.Clone(ctx, mountPoint)
	if err != nil {
		return "", nil, err
	}
	return mountPoint, extra, nil
}

func cleanupAll(cleanups ...func() error) func() error {
	return func() error {
		failures := 0
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] == nil {
				continue
			}
			if err := cleanups[i](); err != nil {
				failures++
			}
		}
		if failures > 0 {
			return fmt.Errorf("%d cleanup operations failed", failures)
		}
		return nil
	}
}
