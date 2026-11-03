package git_test

import (
	"context"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"path/filepath"
)

var _ = Describe("Repository discovery", func() {
	It("discovers an unborn checkout from a missing source path without resolving HEAD", func() {
		root := GinkgoT().TempDir()
		gitFixture(root, "init", "--quiet")
		found, err := git.Discover(GinkgoT().Context(), git.DiscoverOptions{Path: filepath.Join(root, "missing", "source.xml")})
		Expect(err).NotTo(HaveOccurred())
		expected, err := filepath.EvalSymlinks(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Root).To(Equal(expected))
		Expect(found.Bare).To(BeFalse())
	})
	It("recognizes bare repositories and returns caller cancellation", func() {
		fixture := newBareRepo("discover")
		found, err := git.Discover(GinkgoT().Context(), git.DiscoverOptions{Path: fixture.Path})
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Bare).To(BeTrue())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = git.Discover(ctx, git.DiscoverOptions{Path: fixture.Path})
		Expect(err).To(MatchError(context.Canceled))
	})
})
