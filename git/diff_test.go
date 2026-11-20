// Tests the unified patch between two revisions of a cached remote repository, whole or under
// some paths.
package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A cached repository's diff", func() {
	var (
		fixture bareRepo
		cache   *git.Cache
		repo    *git.Repository
	)
	BeforeEach(func() {
		fixture = newBareRepo("diff")
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		repo = mirroredRepository(fixture)
	})

	It("is the unified patch from base to head", func(ctx SpecContext) {
		patch, err := cache.Diff(ctx, git.DiffOptions{Repository: repo, Base: fixture.First, Head: fixture.Feature})
		Expect(err).ToNot(HaveOccurred())
		Expect(patch).To(ContainSubstring("diff --git a/README.md b/README.md"))
		Expect(patch).To(ContainSubstring("-first\n+second\n"))
		Expect(patch).To(ContainSubstring("diff --git a/feature.txt b/feature.txt"))
		Expect(patch).To(ContainSubstring("+feature\n"))
		Expect(patch).ToNot(ContainSubstring("\x1b["), "without color")
	})

	It("keeps only the changes under the paths asked", func(ctx SpecContext) {
		patch, err := cache.Diff(ctx, git.DiffOptions{Repository: repo, Base: fixture.First, Head: fixture.Feature, Paths: []string{"feature.txt"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(patch).To(ContainSubstring("diff --git a/feature.txt b/feature.txt"))
		Expect(patch).ToNot(ContainSubstring("README.md"))
	})

	It("needs both revisions and a repository", func(ctx SpecContext) {
		_, err := cache.Diff(ctx, git.DiffOptions{Repository: repo, Head: fixture.Feature})
		Expect(err).To(MatchError(ContainSubstring("a diff requires base and head revisions")))
		_, err = cache.Diff(ctx, git.DiffOptions{Base: fixture.First, Head: fixture.Feature})
		Expect(err).To(MatchError(ContainSubstring("a diff requires a repository")))
	})
})
