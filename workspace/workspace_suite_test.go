package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWorkspace(t *testing.T) {
	// Specs reason about exactly what each repo ignores: keep the developer's
	// global config and core.excludesFile (~/.config/git/ignore) out of them.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	RegisterFailHandler(Fail)
	RunSpecs(t, "Workspace Suite")
}

func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func lines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func write(dir, name, content string) {
	path := filepath.Join(dir, name)
	ExpectWithOffset(1, os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

func rmFile(dir, name string) error { return os.Remove(filepath.Join(dir, name)) }

func read(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return string(data)
}

// initRepo creates a repo with one commit holding seed.txt and a .gitignore
// that hides ignored.txt and vendor/.
func initRepo() string {
	dir, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	git(dir, "init", "-q", "-b", "main")
	git(dir, "config", "user.email", "test@example.com")
	git(dir, "config", "user.name", "Test")
	write(dir, "seed.txt", "seed\n")
	write(dir, ".gitignore", "ignored.txt\nvendor/\n")
	git(dir, "add", "-A")
	git(dir, "commit", "-q", "-m", "seed")
	return dir
}

// dirtyRepo adds one staged edit, one unstaged edit, one untracked file and
// ignored content — every category a workspace can carry or leave behind.
func dirtyRepo() string {
	dir := initRepo()
	write(dir, "staged.txt", "staged\n")
	git(dir, "add", "staged.txt")
	write(dir, "seed.txt", "seed\nunstaged\n")
	write(dir, "untracked.txt", "untracked\n")
	write(dir, "ignored.txt", "ignored\n")
	write(dir, "vendor/nested/dep.txt", "dep\n")
	return dir
}
