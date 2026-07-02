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

func TestSetupToExecCheckoutModes(t *testing.T) {
	depth := 3
	exec := Setup{
		Cwd: "/repo",
		Checkout: &Checkout{
			Mode:  CheckoutLocal,
			Path:  "/repo",
			Ref:   "feature",
			Depth: &depth,
			Dirty: &Dirty{Stash: StashAll, Since: "main"},
			Worktree: &Worktree{
				Mode:   WorktreeNew,
				Prefix: "captain",
				Base:   "main",
				Keep:   true,
			},
		},
	}.ToExec()

	require.NotNil(t, exec.Checkout)
	assert.Equal(t, "/repo", exec.Checkout.Path)
	assert.Equal(t, "feature", exec.Checkout.Branch)
	assert.Equal(t, &depth, exec.Checkout.Depth)
	require.NotNil(t, exec.Checkout.Dirty)
	assert.True(t, exec.Checkout.Dirty.Staged)
	assert.True(t, exec.Checkout.Dirty.Unstaged)
	assert.True(t, exec.Checkout.Dirty.Untracked)
	assert.Equal(t, "main", exec.Checkout.Dirty.Since)
	require.NotNil(t, exec.Checkout.Worktree)
	assert.Equal(t, "captain", exec.Checkout.Worktree.Prefix)
	assert.Equal(t, "main", exec.Checkout.Worktree.Base)
	assert.True(t, exec.Checkout.Worktree.Keep)
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

func TestSetupEnvLocalWorktreeAppliesDirtyState(t *testing.T) {
	ctx := context.New()
	repo := initShellGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0644))
	runShellGit(t, repo, "add", "staged.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\nunstaged\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0644))

	setup, err := SetupEnv(ctx, &Exec{
		BaseDir: t.TempDir(),
		Checkout: &connection.GitConnection{
			Path: repo,
			Worktree: &connection.GitWorktree{
				Enabled: true,
			},
			Dirty: &connection.GitDirty{
				Staged:    true,
				Unstaged:  true,
				Untracked: true,
			},
		},
	})
	require.NoError(t, err)
	defer setup.Cleanup()

	require.NotEqual(t, repo, setup.Cwd)
	assert.FileExists(t, filepath.Join(setup.Cwd, "staged.txt"))
	assert.FileExists(t, filepath.Join(setup.Cwd, "untracked.txt"))
	assert.Contains(t, string(readFile(t, filepath.Join(setup.Cwd, "seed.txt"))), "unstaged")

	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "diff", "--cached", "--name-only"), "staged.txt")
	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "diff", "--name-only"), "seed.txt")
	assert.Contains(t, gitLinesForTest(t, setup.Cwd, "ls-files", "--others", "--exclude-standard"), "untracked.txt")
	assert.ElementsMatch(t, []string{"seed.txt", "staged.txt", "untracked.txt"}, setup.Extra["dirtyFiles"])
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
