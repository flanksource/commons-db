package git_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Maintain", func() {
	It("expires pins unused for the TTL, collects garbage and re-pins an expired commit on its next read", func(ctx SpecContext) {
		fixture := newBareRepo("maintain")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := mirroredRepository(fixture)
		_, err := cache.Changes(ctx, git.ChangesOptions{Repository: repo, Base: fixture.First, Head: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		repository := filepath.Join(cache.Root, repo.URL.CacheKey())
		stale := time.Now().Add(-2 * time.Hour)
		Expect(os.Chtimes(filepath.Join(repository, "pins", fixture.First), stale, stale)).To(Succeed())
		timed, timings := rpchttp.WithTimings(ctx)
		Expect(cache.Maintain(timed, git.MaintainOptions{PinTTL: time.Hour})).To(Succeed())
		collections, _ := timings.Counter("git_maintenance", "ops")
		Expect(collections).To(Equal(int64(1)))
		pins := gitFixture(filepath.Join(repository, "mirror.git"), "for-each-ref", "--format=%(refname)", "refs/commons-cache/commits/")
		Expect(pins).To(Equal("refs/commons-cache/commits/" + fixture.Main))
		_, err = os.Stat(filepath.Join(repository, "pins", fixture.First))
		Expect(err).To(MatchError(fs.ErrNotExist))
		timed, timings = rpchttp.WithTimings(ctx)
		_, err = cache.Changes(timed, git.ChangesOptions{Repository: repo, Base: fixture.First, Head: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		repins, _ := timings.Counter("git_update_ref", "ops")
		Expect(repins).To(Equal(int64(1)))
	})

	It("rejects a non-positive pin TTL", func(ctx SpecContext) {
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		Expect(cache.Maintain(ctx, git.MaintainOptions{})).To(MatchError(ContainSubstring("pin TTL")))
	})

	It("opens a local tree without copying the repository's refs", func(ctx SpecContext) {
		fixture := newBareRepo("scratch-refs")
		for _, branch := range []string{"one", "two", "three"} {
			gitFixture(fixture.Path, "branch", branch, fixture.First)
		}
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		timed, timings := rpchttp.WithTimings(ctx)
		tree, err := cache.OpenTree(timed, git.TreeOptions{Directory: fixture.Path, Revision: "feature"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		Expect(tree.Info().SHA).To(Equal(fixture.Feature))
		body, err := fs.ReadFile(tree, "feature.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("feature\n"))
		listed, _ := timings.Counter("git_for_each_ref", "ops")
		Expect(listed).To(BeZero())
	})
})
