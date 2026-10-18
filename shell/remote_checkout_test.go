package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prepareRemoteCheckout", func() {
	gitIn := func(dir string, args ...string) string {
		GinkgoHelper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.org",
			"GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.org")
		out, err := cmd.CombinedOutput()
		Expect(err).ToNot(HaveOccurred(), string(out))
		return strings.TrimSpace(string(out))
	}

	It("clones the ?ref= of the checkout URL and reports the short URL and commit", func() {
		root := GinkgoT().TempDir()
		work := filepath.Join(root, "work")
		Expect(os.MkdirAll(work, 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		Expect(os.WriteFile(filepath.Join(work, "a.txt"), []byte("main\n"), 0o644)).To(Succeed())
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "main")
		gitIn(work, "checkout", "-q", "-b", "release")
		Expect(os.WriteFile(filepath.Join(work, "a.txt"), []byte("release\n"), 0o644)).To(Succeed())
		gitIn(work, "commit", "-q", "-am", "release")
		release := gitIn(work, "rev-parse", "HEAD")
		bare := filepath.Join(root, "repo.git")
		gitIn(root, "clone", "-q", "--bare", work, bare)

		checkout := connection.GitConnection{URL: "file://" + bare + "?ref=release", Branch: "main"}
		mountPoint, extra, err := prepareRemoteCheckout(context.New(), GinkgoT().TempDir(), &checkout)
		Expect(err).ToNot(HaveOccurred())
		Expect(extra).To(Equal(map[string]any{"git": bare, "commit": release}))
		Expect(os.ReadFile(filepath.Join(mountPoint, "a.txt"))).To(BeEquivalentTo("release\n"))
		Expect(gitIn(mountPoint, "rev-list", "--count", "HEAD")).To(Equal("1"))
	})
})
