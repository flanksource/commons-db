package git_test

import (
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("git cache administration", func() {
	var cache *git.Cache
	var donor, fork *git.Repository
	var forkFixture bareRepo
	BeforeEach(func(ctx SpecContext) {
		donorFixture, sibling, remote := organizationFixtures()
		forkFixture = sibling
		donor = organizationRepository(remote, donorFixture, "example")
		fork = organizationRepository(remote, forkFixture, "example")
		cache = &git.Cache{Root: fixtureTempDir()}
		Expect(cache.Fetch(ctx, donor, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
	})

	It("reports independent mirrors and prevents eviction of a leased borrower", func(ctx SpecContext) {
		tree, err := cache.Checkout(ctx, fork, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		entries, err := cache.Inventory(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Pool).NotTo(BeEmpty())
		Expect(entries[0].Pool).To(Equal(entries[1].Pool))
		Expect(entries).To(ContainElement(MatchFields(IgnoreExtras, Fields{
			"Key": Equal(fork.URL.CacheKey()), "Owner": Equal(fork.URL.CacheKey()), "Mirror": Not(BeNil()),
			"Worktrees": Equal([]git.WorktreeState{{Name: filepath.Base(tree.Dir), Leased: true}}), "Error": BeEmpty(),
		})))
		_, err = cache.Evict(ctx, git.EvictOptions{Owner: fork.URL.CacheKey()})
		Expect(err).To(MatchError(git.ErrCacheBusy))
		result, err := cache.Evict(ctx, git.EvictOptions{Owner: donor.URL.CacheKey()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(git.EvictResult{Owner: donor.URL.CacheKey()}))
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Nanosecond})).To(Succeed())
		Expect(gitFixture(tree.Dir, "rev-parse", "HEAD")).To(Equal(forkFixture.Main))
		gitFixture(tree.Dir, "fsck", "--full")
	})

	It("prunes only the named mirror and returns no rebind candidates", func(ctx SpecContext) {
		Expect(cache.Fetch(ctx, fork, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Nanosecond, Owners: []string{donor.URL.CacheKey()}})).To(Succeed())
		entries, err := cache.Inventory(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{"Key": Equal(donor.URL.CacheKey()), "Mirror": PointTo(MatchFields(IgnoreExtras, Fields{"Pins": BeZero()}))}),
			MatchFields(IgnoreExtras, Fields{"Key": Equal(fork.URL.CacheKey()), "Mirror": PointTo(MatchFields(IgnoreExtras, Fields{"Pins": Equal(1)}))}),
		))
		candidates, err := cache.SharedOwners(ctx, fork)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(BeEmpty())
		_, err = cache.Rebind(ctx, fork, git.RebindOptions{Owner: donor.URL.CacheKey()})
		Expect(err).To(MatchError(git.ErrSharedCacheUnsupported))
	})

	DescribeTable("rejects invalid eviction keys", func(ctx SpecContext, key string) {
		_, err := cache.Evict(ctx, git.EvictOptions{Owner: key})
		Expect(err).To(MatchError(git.ErrCacheEntry))
	}, Entry("parent traversal", "../escape"), Entry("a nested path", "owner/mirror.git"), Entry("an absolute path", "/var/cache"), Entry("the root itself", "."))
})
