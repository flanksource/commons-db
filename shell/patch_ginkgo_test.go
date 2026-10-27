package shell

import (
	"os"
	"os/exec"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("supplied workspace patches", func() {
	It("preserves a checkout supplied through a named connection", func() {
		setup := Setup{Checkout: &Checkout{Connection: "connection://repository"}}
		Expect(setup.ToExec().Checkout).NotTo(BeNil())
		Expect(setup.ToExec().Checkout.Connection).To(Equal("connection://repository"))
	})
	const patch = "diff --git a/seed.txt b/seed.txt\n--- a/seed.txt\n+++ b/seed.txt\n@@ -1 +1 @@\n-seed\n+reviewed\n"
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
		return string(out)
	}
	var source, base string
	BeforeEach(func() {
		source, base = GinkgoT().TempDir(), GinkgoT().TempDir()
		git(source, "init", "-q", "-b", "main")
		git(source, "config", "user.email", "test@example.com")
		git(source, "config", "user.name", "Test")
		Expect(os.WriteFile(filepath.Join(source, "seed.txt"), []byte("seed\n"), 0o600)).To(Succeed())
		git(source, "add", "seed.txt")
		git(source, "commit", "-qm", "seed")
	})
	setup := func(patch string) *Setup {
		return &Setup{BaseDir: base, Patch: patch, Checkout: &Checkout{
			Mode: CheckoutLocal, Path: source, Worktree: &Worktree{Mode: WorktreeNew, Base: "HEAD"},
		}}
	}
	It("applies the patch only to the new worktree", func() {
		result, err := Prepare(dbcontext.New(), setup(patch))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(result.Cleanup)
		data, err := os.ReadFile(filepath.Join(result.Cwd, "seed.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("reviewed\n"))
		original, err := os.ReadFile(filepath.Join(source, "seed.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(original)).To(Equal("seed\n"))
	})
	It("refuses to apply a patch to an existing directory", func() {
		_, err := Prepare(dbcontext.New(), &Setup{Cwd: source, BaseDir: base, Patch: patch})
		Expect(err).To(MatchError(ContainSubstring("isolated")))
		Expect(git(source, "status", "--porcelain")).To(BeEmpty())
	})
	It("fails a conflicting patch and removes the allocated worktree", func() {
		input := setup(patch)
		input.Patch = "diff --git a/missing.txt b/missing.txt\n--- a/missing.txt\n+++ b/missing.txt\n@@ -1 +1 @@\n-missing\n+changed\n"
		_, err := Prepare(dbcontext.New(), input)
		Expect(err).To(HaveOccurred())
		Expect(git(source, "worktree", "list", "--porcelain")).NotTo(ContainSubstring(base))
	})
})
