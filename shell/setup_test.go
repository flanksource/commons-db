package shell

import (
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupEnvDotEnvPrecedence(t *testing.T) {
	ctx := context.New()
	baseDir := t.TempDir()
	first := filepath.Join(baseDir, "first.env")
	second := filepath.Join(baseDir, "second.env")
	require.NoError(t, os.WriteFile(first, []byte("A=one\nSHARED=first\n"), 0644))
	require.NoError(t, os.WriteFile(second, []byte("B=two\nSHARED=second\nexport EXPORTED=yes\nYAML_STYLE: ok\n"), 0644))

	setup, err := SetupEnv(ctx, &Exec{
		BaseDir: baseDir,
		DotEnv:  []string{first, second},
		EnvVars: []types.EnvVar{
			{Name: "SHARED", ValueStatic: "typed"},
		},
	})
	require.NoError(t, err)
	defer setup.Cleanup()

	env := envSliceMap(setup.Env)
	assert.Equal(t, "one", env["A"])
	assert.Equal(t, "two", env["B"])
	assert.Equal(t, "yes", env["EXPORTED"])
	assert.Equal(t, "ok", env["YAML_STYLE"])
	assert.Equal(t, "typed", env["SHARED"])
	assert.NotEmpty(t, setup.Cwd)
}

func TestPrepareDotEnvPrecedence(t *testing.T) {
	ctx := context.New()
	baseDir := t.TempDir()
	first := filepath.Join(baseDir, "first.env")
	second := filepath.Join(baseDir, "second.env")
	require.NoError(t, os.WriteFile(first, []byte("A=one\nSHARED=first\n"), 0644))
	require.NoError(t, os.WriteFile(second, []byte("B=two\nSHARED=second\n"), 0644))

	setup, err := Prepare(ctx, &Setup{
		BaseDir: baseDir,
		DotEnv:  []string{first, second},
		EnvVars: []types.EnvVar{
			{Name: "SHARED", ValueStatic: "typed"},
		},
	})
	require.NoError(t, err)
	defer setup.Cleanup()

	env := envSliceMap(setup.Env)
	assert.Equal(t, "one", env["A"])
	assert.Equal(t, "two", env["B"])
	assert.Equal(t, "typed", env["SHARED"])
	assert.NotEmpty(t, setup.Cwd)
}

func TestSetupResolveMakesRelativePathsExplicitWithoutMutatingInput(t *testing.T) {
	baseDir := t.TempDir()
	setup := Setup{
		Cwd:     "workspace",
		BaseDir: ".runtime",
		DotEnv:  []string{".env", "config/local.env"},
		Checkout: &Checkout{
			Mode: CheckoutLocal,
			Path: "source",
			Worktree: &Worktree{
				Mode: WorktreeExisting,
				Path: "worktrees/feature",
			},
		},
	}

	resolved, err := setup.Resolve(baseDir)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(baseDir, "workspace"), resolved.Cwd)
	assert.Equal(t, filepath.Join(baseDir, ".runtime"), resolved.BaseDir)
	assert.Equal(t, []string{
		filepath.Join(baseDir, ".env"),
		filepath.Join(baseDir, "config/local.env"),
	}, resolved.DotEnv)
	require.NotNil(t, resolved.Checkout)
	assert.Equal(t, filepath.Join(baseDir, "source"), resolved.Checkout.Path)
	require.NotNil(t, resolved.Checkout.Worktree)
	assert.Equal(t, filepath.Join(baseDir, "worktrees/feature"), resolved.Checkout.Worktree.Path)

	assert.Equal(t, "workspace", setup.Cwd)
	assert.Equal(t, ".runtime", setup.BaseDir)
	assert.Equal(t, []string{".env", "config/local.env"}, setup.DotEnv)
	assert.Equal(t, "source", setup.Checkout.Path)
	assert.Equal(t, "worktrees/feature", setup.Checkout.Worktree.Path)
}

// Resolve promises "an independent setup". A caller that edits the resolved copy
// — the setup hook clears Checkout, a runner overrides one connection — must not
// reach back into the config the setup was resolved from, which is typically
// shared by every run in the process.
func TestSetupResolveSharesNoMutableStateWithTheOriginal(t *testing.T) {
	const (
		originalConfigItem = "config-item-original"
		originalRegion     = "eu-west-1"
		originalSecretName = "secret-original"
	)
	configItem := originalConfigItem
	depth := 3
	setup := Setup{
		EnvVars: []types.EnvVar{{
			Name:      "TOKEN",
			ValueFrom: &types.EnvVarSource{SecretKeyRef: &types.SecretKeySelector{Key: originalSecretName}},
		}},
		Connections: connection.ExecConnections{
			FromConfigItem: &configItem,
			AWS:            &connection.AWSConnection{Region: originalRegion},
		},
		Checkout: &Checkout{
			Mode:     CheckoutLocal,
			Path:     "source",
			Depth:    &depth,
			Worktree: &Worktree{Mode: WorktreeNew, Uncommitted: CloneClone},
		},
	}

	resolved, err := setup.Resolve(t.TempDir())
	require.NoError(t, err)

	*resolved.Connections.FromConfigItem = "config-item-mutated"
	resolved.Connections.AWS.Region = "us-east-1"
	resolved.EnvVars[0].ValueFrom.SecretKeyRef.Key = "secret-mutated"
	*resolved.Checkout.Depth = depth + 1
	resolved.Checkout.Worktree.Uncommitted = CloneSkip
	resolved.Checkout.Worktree.Mode = WorktreeExisting

	assert.Equal(t, originalConfigItem, *setup.Connections.FromConfigItem)
	assert.Equal(t, originalRegion, setup.Connections.AWS.Region)
	assert.Equal(t, originalSecretName, setup.EnvVars[0].ValueFrom.SecretKeyRef.Key)
	assert.Equal(t, depth, *setup.Checkout.Depth)
	assert.Equal(t, CloneClone, setup.Checkout.Worktree.Uncommitted)
	assert.Equal(t, WorktreeNew, setup.Checkout.Worktree.Mode)
}

func TestSetupResolveDefaultsWorkspaceAndRuntimeDirectory(t *testing.T) {
	baseDir := t.TempDir()

	resolved, err := (Setup{}).Resolve(baseDir)
	require.NoError(t, err)

	assert.Equal(t, baseDir, resolved.Cwd)
	assert.Equal(t, filepath.Join(baseDir, ".shell"), resolved.BaseDir)
}

func TestSetupToExecCheckoutModes(t *testing.T) {
	depth := 3
	exec := Setup{
		Cwd: "/repo",
		Checkout: &Checkout{
			Mode:  CheckoutLocal,
			Path:  "/repo",
			Ref:   "feature",
			Depth: &depth,
			Since: "main",
			Worktree: &Worktree{
				Mode:        WorktreeNew,
				Prefix:      "captain",
				Base:        "main",
				Keep:        true,
				Uncommitted: CloneClone,
				Ignored:     CloneSkip,
			},
		},
	}.ToExec()

	require.NotNil(t, exec.Checkout)
	assert.Equal(t, "/repo", exec.Checkout.Path)
	assert.Equal(t, "feature", exec.Checkout.Branch)
	assert.Equal(t, &depth, exec.Checkout.Depth)
	assert.Equal(t, "main", exec.Checkout.Since)
	require.NotNil(t, exec.Checkout.Worktree)
	assert.Equal(t, "captain", exec.Checkout.Worktree.Prefix)
	assert.Equal(t, "main", exec.Checkout.Worktree.Base)
	assert.True(t, exec.Checkout.Worktree.Keep)
	assert.True(t, exec.Checkout.Worktree.Uncommitted)
	assert.False(t, exec.Checkout.Worktree.Ignored)
}

func TestWorktreeApplyDefaults(t *testing.T) {
	tests := []struct {
		name        string
		worktree    Worktree
		want        Worktree
		wantWarning bool
	}{
		{
			name:     "bare new worktree carries everything from HEAD",
			worktree: Worktree{Mode: WorktreeNew},
			want:     Worktree{Mode: WorktreeNew, Base: "HEAD", Uncommitted: CloneClone, Ignored: CloneClone},
		},
		{
			name:        "non-HEAD base degrades uncommitted to skip",
			worktree:    Worktree{Mode: WorktreeNew, Base: "main"},
			want:        Worktree{Mode: WorktreeNew, Base: "main", Uncommitted: CloneSkip, Ignored: CloneClone},
			wantWarning: true,
		},
		{
			name:     "explicit uncommitted survives a non-HEAD base",
			worktree: Worktree{Mode: WorktreeNew, Base: "main", Uncommitted: CloneClone},
			want:     Worktree{Mode: WorktreeNew, Base: "main", Uncommitted: CloneClone, Ignored: CloneClone},
		},
		{
			name:     "explicit values are never overwritten",
			worktree: Worktree{Mode: WorktreeNew, Base: "v1.2.3", Prefix: "custom", Uncommitted: CloneSkip, Ignored: CloneSkip},
			want:     Worktree{Mode: WorktreeNew, Base: "v1.2.3", Prefix: "custom", Uncommitted: CloneSkip, Ignored: CloneSkip},
		},
		{
			name:     "mode none is left untouched",
			worktree: Worktree{Mode: WorktreeNone},
			want:     Worktree{Mode: WorktreeNone},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.worktree
			warnings := got.ApplyDefaults()
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarning, len(warnings) > 0, "warnings: %v", warnings)
		})
	}
}

func TestSetupToExecExistingWorktree(t *testing.T) {
	exec := Setup{
		Cwd: "/repo",
		Checkout: &Checkout{
			Mode: CheckoutLocal,
			Path: "/repo",
			Worktree: &Worktree{
				Mode: WorktreeExisting,
				Path: "/repo-worktree",
			},
		},
	}.ToExec()

	require.NotNil(t, exec.Checkout)
	assert.Equal(t, "/repo-worktree", exec.Checkout.Path)
	assert.Nil(t, exec.Checkout.Worktree)
}

func TestSetupEnvLocalGitPath(t *testing.T) {
	ctx := context.New()
	repo := initShellGitRepo(t)
	repo = canonicalPath(t, repo)

	setup, err := SetupEnv(ctx, &Exec{
		BaseDir: t.TempDir(),
		Checkout: &connection.GitConnection{
			Path: repo,
		},
	})
	require.NoError(t, err)
	defer setup.Cleanup()

	assert.Equal(t, repo, setup.Cwd)
	assert.Equal(t, "local", setup.Extra["git"])
	assert.Equal(t, repo, setup.Extra["path"])
	assert.NotEmpty(t, setup.Extra["commit"])
}

// dirtyShellGitRepo seeds a repo with one staged edit, one unstaged edit, one
// untracked file and one gitignored file — the four categories a worktree can
// carry or leave behind.
func dirtyShellGitRepo(t *testing.T) string {
	t.Helper()
	repo := initShellGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.txt\nvendor/\n"), 0644))
	runShellGit(t, repo, "add", ".gitignore")
	runShellGit(t, repo, "commit", "-q", "-m", "gitignore")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0644))
	runShellGit(t, repo, "add", "staged.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\nunstaged\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "ignored.txt"), []byte("ignored\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "vendor", "nested"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "vendor", "nested", "dep.txt"), []byte("dep\n"), 0644))
	return repo
}

func worktreeSetup(t *testing.T, repo string, wt *connection.GitWorktree) *SetupResult {
	t.Helper()
	setup, err := SetupEnv(context.New(), &Exec{
		BaseDir:  t.TempDir(),
		Checkout: &connection.GitConnection{Path: repo, Worktree: wt},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = setup.Cleanup() })
	require.NotEqual(t, repo, setup.Cwd)
	return setup
}

func TestSetupEnvWorktreeCarriesUncommittedAndIgnored(t *testing.T) {
	repo := dirtyShellGitRepo(t)
	before := gitLinesForTest(t, repo, "status", "--porcelain")

	setup := worktreeSetup(t, repo, &connection.GitWorktree{
		Enabled: true, Uncommitted: true, Ignored: true,
	})

	assert.FileExists(t, filepath.Join(setup.Cwd, "staged.txt"))
	assert.FileExists(t, filepath.Join(setup.Cwd, "untracked.txt"))
	assert.FileExists(t, filepath.Join(setup.Cwd, "ignored.txt"))
	assert.FileExists(t, filepath.Join(setup.Cwd, "vendor", "nested", "dep.txt"))
	assert.Contains(t, string(readFile(t, filepath.Join(setup.Cwd, "seed.txt"))), "unstaged")

	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "diff", "--cached", "--name-only"), "staged.txt")
	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "diff", "--name-only"), "seed.txt")
	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "ls-files", "--others", "--exclude-standard"), "untracked.txt")
	assert.ElementsMatch(t, []string{"seed.txt", "staged.txt", "untracked.txt"}, setup.Extra["dirtyFiles"])

	// The source repo is read-only throughout: nothing is stashed, so a crashed
	// run can never strand the developer's work.
	assert.Equal(t, before, gitLinesForTest(t, repo, "status", "--porcelain"))
}

func TestSetupEnvWorktreeSkipsUncommittedAndIgnored(t *testing.T) {
	repo := dirtyShellGitRepo(t)

	setup := worktreeSetup(t, repo, &connection.GitWorktree{Enabled: true})

	assert.NoFileExists(t, filepath.Join(setup.Cwd, "staged.txt"))
	assert.NoFileExists(t, filepath.Join(setup.Cwd, "untracked.txt"))
	assert.NoFileExists(t, filepath.Join(setup.Cwd, "ignored.txt"))
	assert.NoFileExists(t, filepath.Join(setup.Cwd, "vendor", "nested", "dep.txt"))
	assert.NotContains(t, string(readFile(t, filepath.Join(setup.Cwd, "seed.txt"))), "unstaged")
	assert.Empty(t, gitLinesForTest(t, setup.Cwd, "status", "--porcelain"))
}

func TestSetupEnvWorktreeIgnoredOnlySkipsUncommitted(t *testing.T) {
	repo := dirtyShellGitRepo(t)

	setup := worktreeSetup(t, repo, &connection.GitWorktree{Enabled: true, Ignored: true})

	assert.FileExists(t, filepath.Join(setup.Cwd, "ignored.txt"))
	assert.FileExists(t, filepath.Join(setup.Cwd, "vendor", "nested", "dep.txt"))
	assert.NoFileExists(t, filepath.Join(setup.Cwd, "staged.txt"))
	assert.NoFileExists(t, filepath.Join(setup.Cwd, "untracked.txt"))
	// .git/ is never copied — the worktree has its own gitdir pointer file.
	assert.NoDirExists(t, filepath.Join(setup.Cwd, ".git", "refs"))
}

// Copies must be independent of the source: a copy-on-write clone is fine, a
// hardlink is not — editing the worktree copy would corrupt the source repo.
func TestSetupEnvWorktreeCopiesAreIndependentOfSource(t *testing.T) {
	repo := dirtyShellGitRepo(t)

	setup := worktreeSetup(t, repo, &connection.GitWorktree{
		Enabled: true, Uncommitted: true, Ignored: true,
	})

	for _, name := range []string{"untracked.txt", "ignored.txt"} {
		copied := filepath.Join(setup.Cwd, name)
		require.NoError(t, os.WriteFile(copied, []byte("rewritten by the worktree\n"), 0644))
		assert.NotContains(t, string(readFile(t, filepath.Join(repo, name))), "rewritten",
			"%s in the source repo was modified through its worktree copy", name)
	}
}

func initShellGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runShellGit(t, dir, "init", "-q", "-b", "main")
	runShellGit(t, dir, "config", "user.email", "test@example.com")
	runShellGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0644))
	runShellGit(t, dir, "add", "-A")
	runShellGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return canonical
}

func runShellGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osExec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

func gitLinesForTest(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	cmd := osExec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err, "git %v failed", args)
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func envSliceMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
