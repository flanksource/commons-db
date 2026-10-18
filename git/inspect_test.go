package git_test

import (
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Inspect", func() {
	It("reports provenance and dirty counts of a checkout", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		dir := filepath.Join(GinkgoT().TempDir(), "checkout")
		_, err := git.Clone(ctx, newRepository(fixture.FileURL(), git.Auth{}), git.CloneOptions{Dir: dir})
		Expect(err).ToNot(HaveOccurred())
		writeFixtureFile(dir, "README.md", "changed\n")
		writeFixtureFile(dir, "untracked.txt", "new\n")

		metadata, err := git.Inspect(ctx, dir, git.InspectOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(metadata.Repository).To(Equal(fixture.FileURL()))
		Expect(metadata.Branch).To(Equal("main"))
		Expect(metadata.SHA).To(Equal(fixture.Main[:12]))
		Expect(metadata.Dirty).To(Equal(git.DirtyStats{Unstaged: 1, Untracked: 1}))
		Expect(metadata.LastCommit.Subject).To(Equal("second"))

		_, err = git.Inspect(ctx, GinkgoT().TempDir(), git.InspectOptions{})
		Expect(err).To(MatchError(git.ErrNotRepository))
	})
})
