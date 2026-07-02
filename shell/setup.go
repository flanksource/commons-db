package shell

import (
	"fmt"
	"os"
	osExec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/hash"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"github.com/flanksource/commons/utils"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/samber/lo"
)

var checkoutLocks = utils.NamedLock{}

type SetupResult struct {
	Env       []string
	Cwd       string
	Extra     map[string]any
	Artifacts []Artifact
	Cleanup   func() error
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
	Dirty      *Dirty       `json:"dirty,omitempty" yaml:"dirty,omitempty"`
	Worktree   *Worktree    `json:"worktree,omitempty" yaml:"worktree,omitempty"`
}

type CheckoutMode string

const (
	CheckoutNone   CheckoutMode = "none"
	CheckoutLocal  CheckoutMode = "local"
	CheckoutRemote CheckoutMode = "remote"
)

type Dirty struct {
	Stash StashMode `json:"stash,omitempty" yaml:"stash,omitempty"`
	Since string    `json:"since,omitempty" yaml:"since,omitempty"`
}

type StashMode string

const (
	StashNone      StashMode = "none"
	StashUntracked StashMode = "untracked"
	StashUnstaged  StashMode = "unstaged"
	StashStaged    StashMode = "staged"
	StashAll       StashMode = "all"
)

type Worktree struct {
	Mode   WorktreeMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	Prefix string       `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Base   string       `json:"base,omitempty" yaml:"base,omitempty"`
	Path   string       `json:"path,omitempty" yaml:"path,omitempty"`
	Keep   bool         `json:"keep,omitempty" yaml:"keep,omitempty"`
}

type WorktreeMode string

const (
	WorktreeNone     WorktreeMode = "none"
	WorktreeNew      WorktreeMode = "new"
	WorktreeExisting WorktreeMode = "existing"
)

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
		Dirty:      c.Dirty.toGitDirty(),
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

func (d *Dirty) toGitDirty() *connection.GitDirty {
	if d == nil {
		return nil
	}
	out := &connection.GitDirty{Since: d.Since}
	switch d.Stash {
	case StashUntracked:
		out.Untracked = true
	case StashUnstaged:
		out.Unstaged = true
	case StashStaged:
		out.Staged = true
	case StashAll:
		out.Staged = true
		out.Unstaged = true
		out.Untracked = true
	}
	if out.IsEmpty() {
		return nil
	}
	return out
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
		Enabled: true,
		Prefix:  w.Prefix,
		Base:    w.Base,
		Keep:    w.Keep,
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
			return nil, err
		}
	}

	envs, err := buildEnv(ctx, *exec, envParams.envs)
	if err != nil {
		return nil, err
	}

	cmd := osExec.CommandContext(ctx, "true")
	cmd.Dir = cwd
	cmd.Env = envs

	setupResult, err := connection.SetupConnection(ctx, exec.Connections, cmd)
	if err != nil {
		return nil, ctx.Oops().Wrap(err)
	}
	ctx = ctx.WithLoggingValues("connection", setupResult)

	cleanup := cleanupAll(workspaceCleanup, func() error {
		if waitBeforeCleanup := ctx.Properties().Duration("shell.connection.wait_before_cleanup", 0); waitBeforeCleanup > 0 {
			time.Sleep(waitBeforeCleanup)
		}
		if setupResult.Cleanup == nil {
			return nil
		}
		return setupResult.Cleanup()
	})

	return &SetupResult{
		Env:       mergeEnvSlices(cmd.Env),
		Cwd:       cwd,
		Extra:     envParams.extra,
		Artifacts: exec.Artifacts,
		Cleanup:   cleanup,
	}, nil
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

func buildEnv(ctx context.Context, exec Exec, resolvedEnv []string) ([]string, error) {
	dotenv, err := loadDotEnv(exec.DotEnv...)
	if err != nil {
		return nil, err
	}

	return mergeEnvSlices(
		allowedHostEnv(),
		envMapToSlice(dotenv),
		resolvedEnv,
	), nil
}

func allowedHostEnv() []string {
	var envs []string
	for _, e := range os.Environ() {
		key, _, ok := strings.Cut(e, "=")
		if _, exists := allowedEnvVars[key]; exists && ok {
			envs = append(envs, e)
		}
	}
	return envs
}

func loadDotEnv(paths ...string) (map[string]string, error) {
	merged := map[string]string{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		env, err := godotenv.Read(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read dotenv %s: %w", path, err)
		}
		for k, v := range env {
			merged[k] = v
		}
	}
	return merged, nil
}

func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return out
}

func mergeEnvSlices(layers ...[]string) []string {
	values := map[string]string{}
	var order []string
	for _, layer := range layers {
		for _, e := range layer {
			key, value, ok := strings.Cut(e, "=")
			if !ok || key == "" {
				continue
			}
			if _, seen := values[key]; !seen {
				order = append(order, key)
			}
			values[key] = value
		}
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return out
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
		var errs []error
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] == nil {
				continue
			}
			if err := cleanups[i](); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			logger.Errorf("shell setup cleanup errors: %v", errs)
			return fmt.Errorf("cleanup failed: %v", errs)
		}
		return nil
	}
}
