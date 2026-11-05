package git_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("git cache administration", func() {
	var (
		cache              *git.Cache
		donor, fork        bareRepo
		donorRepo          *git.Repository
		forkRepo           *git.Repository
		forkTip            string
		donorKey, forkKey  string
		forkHistory        *historyFixtureProvider
		mirrorOf           func(key string) string
		recordedFor        func(mirror, key string) string
		readForkThroughKey func(ctx SpecContext)
	)
	BeforeEach(func(ctx SpecContext) {
		donor = newBareRepo("donor")
		fork = newFork(donor)
		forkTip = fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
		cache = &git.Cache{Root: fixtureTempDir()}
		donorRepo = mirroredRepository(donor)
		donorRepo.Provider = &historyFixtureProvider{}
		tree, err := cache.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Release()).To(Succeed())
		forkRepo = mirroredRepository(fork)
		forkHistory = &historyFixtureProvider{base: donor.Main}
		forkRepo.Provider = forkHistory
		donorKey, forkKey = donorRepo.URL.CacheKey(), forkRepo.URL.CacheKey()
		mirrorOf = func(key string) string { return filepath.Join(cache.Root, key, "mirror.git") }
		recordedFor = func(mirror, key string) string {
			return gitFixture(mirror, "for-each-ref", "--format=%(objectname)", "refs/commons-cache/repos/"+key+"/")
		}
		readForkThroughKey = func(ctx SpecContext) {
			GinkgoHelper()
			data, err := cache.Show(ctx, forkRepo, forkTip, "fork.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("fork\n"))
		}
	})

	It("describes owners with their dependents, recorded commits, pins and leased worktrees", func(ctx SpecContext) {
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)

		entries, err := cache.Inventory(ctx)
		Expect(err).NotTo(HaveOccurred())
		recorded := func(sha string) OmegaMatcher {
			return MatchAllFields(Fields{"Ref": Equal("heads/main"), "SHA": Equal(sha), "Committed": Not(BeZero())})
		}
		Expect(entries).To(ConsistOf(
			MatchAllFields(Fields{
				"Key": Equal(donorKey), "Repository": Equal(donorRepo.URL.Redacted()), "Owner": Equal(donorKey),
				"BoundAt": Not(BeNil()), "Dependents": Equal([]string{forkKey}),
				"Mirror": PointTo(MatchAllFields(Fields{
					"SizeBytes": BeNumerically(">", 0), "Objects": BeNumerically(">", 0), "Packs": BeNumerically(">=", 0),
					"Shallow": BeFalse(), "Pins": Equal(2), "LastUsed": Not(BeNil()), "OldestUse": Not(BeNil()), "Locked": BeFalse(),
				})),
				"Commits": ConsistOf(recorded(donor.Main)), "Worktrees": BeEmpty(), "Error": BeEmpty(),
			}),
			MatchAllFields(Fields{
				"Key": Equal(forkKey), "Repository": Equal(forkRepo.URL.Redacted()), "Owner": Equal(donorKey),
				"BoundAt": Not(BeNil()), "Dependents": BeEmpty(), "Mirror": BeNil(),
				"Commits":   ConsistOf(recorded(forkTip)),
				"Worktrees": Equal([]git.WorktreeState{{Name: filepath.Base(tree.Dir), Leased: true}}), "Error": BeEmpty(),
			}),
		))
	})

	It("prunes only the named mirrors and refuses one that does not exist", func(ctx SpecContext) {
		readForkThroughKey(ctx)
		other := cacheMirror(ctx, cache, newBareRepo("other"))

		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Nanosecond, Owners: []string{donorKey}})).To(Succeed())
		Expect(gitFixture(mirrorOf(donorKey), "for-each-ref", "refs/commons-cache/commits/")).To(BeEmpty())
		Expect(gitFixture(mirrorOf(other.URL.CacheKey()), "for-each-ref", "refs/commons-cache/commits/")).NotTo(BeEmpty())
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour, Owners: []string{forkKey}})).To(MatchError(ContainSubstring(forkKey)))
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour, Owners: []string{"../escape"}})).To(MatchError(ContainSubstring("invalid mirror")))
	})

	Describe("evicting a mirror", func() {
		It("removes it with its dependents' bindings, and the next read clones again", func(ctx SpecContext) {
			readForkThroughKey(ctx)

			result, err := cache.Evict(ctx, git.EvictOptions{Owner: donorKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(git.EvictResult{Owner: donorKey, Dependents: []string{forkKey}}))
			Expect(mirrorOf(donorKey)).NotTo(BeAnExistingFile())
			Expect(filepath.Join(cache.Root, forkKey, "repository.json")).NotTo(BeAnExistingFile())

			tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(tree.Release)
			Expect(gitFixture(tree.Dir, "show", "HEAD:fork.txt")).To(Equal("fork"))
			Expect(cacheOwner(cache, forkRepo)).To(Equal(forkKey))
		})

		It("rediscovers a binding whose owner's mirror is gone", func(ctx SpecContext) {
			Expect(os.MkdirAll(filepath.Join(cache.Root, forkKey), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(cache.Root, forkKey, "repository.json"), []byte(`{"version":1,"owner":"evicted-owner"}`), 0o600)).To(Succeed())

			readForkThroughKey(ctx)
			Expect(cacheOwner(cache, forkRepo)).To(Equal(donorKey))
		})

		It("refuses while a dependent's worktree is leased", func(ctx SpecContext) {
			tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main"})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(tree.Release)

			_, err = cache.Evict(ctx, git.EvictOptions{Owner: donorKey})
			Expect(err).To(MatchError(git.ErrCacheBusy))
			Expect(mirrorOf(donorKey)).To(BeADirectory())
			Expect(gitFixture(tree.Dir, "show", "HEAD:fork.txt")).To(Equal("fork"))
		})

		It("refuses a repository reading another repository's mirror", func(ctx SpecContext) {
			readForkThroughKey(ctx)
			_, err := cache.Evict(ctx, git.EvictOptions{Owner: forkKey})
			Expect(err).To(MatchError(git.ErrCacheEntry))
		})
	})

	Describe("rebinding a repository", func() {
		var altRepo *git.Repository
		var altKey, altTip string
		BeforeEach(func(ctx SpecContext) {
			readForkThroughKey(ctx)
			alt := newFork(donor)
			altTip = alt.commitOn(fixtureCommit{Branch: "main", Path: "alt.txt", Content: "alt\n", Message: "alt"})
			altRepo = cacheMirror(ctx, cache, alt)
			altKey = altRepo.URL.CacheKey()
		})

		It("moves it onto the named mirror and forgets it in the previous one", func(ctx SpecContext) {
			result, err := cache.Rebind(ctx, forkRepo, git.RebindOptions{Owner: altKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(git.RebindResult{Key: forkKey, Previous: donorKey, Owner: altKey}))
			Expect(cacheOwner(cache, forkRepo)).To(Equal(altKey))
			Expect(recordedFor(mirrorOf(donorKey), forkKey)).To(BeEmpty())
			Expect(recordedFor(mirrorOf(altKey), forkKey)).To(Equal(forkTip))
			readForkThroughKey(ctx)
		})

		It("rediscovers the mirror sharing the most history", func(ctx SpecContext) {
			forkHistory.bases = map[string]string{donor.Main: donor.First, forkTip: donor.First, altTip: donor.Main}

			owners, err := cache.SharedOwners(ctx, forkRepo)
			Expect(err).NotTo(HaveOccurred())
			Expect(owners).To(HaveLen(2))
			Expect(owners[0]).To(MatchAllFields(Fields{"Owner": Equal(altKey), "Base": Equal(donor.Main), "Commits": Equal(2), "Committed": Not(BeZero())}))
			Expect(owners[1]).To(MatchAllFields(Fields{"Owner": Equal(donorKey), "Base": Equal(donor.First), "Commits": Equal(1), "Committed": Not(BeZero())}))

			result, err := cache.Rebind(ctx, forkRepo, git.RebindOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(git.RebindResult{Key: forkKey, Previous: donorKey, Owner: altKey}))
		})

		It("refuses a mirror sharing no history and keeps the binding", func(ctx SpecContext) {
			forkHistory.bases = map[string]string{}
			_, err := cache.Rebind(ctx, forkRepo, git.RebindOptions{Owner: altKey})
			Expect(err).To(MatchError(git.ErrUnrelatedHistories))
			Expect(cacheOwner(cache, forkRepo)).To(Equal(donorKey))
		})

		It("refuses a repository owning its mirror, and one whose worktree is leased", func(ctx SpecContext) {
			_, err := cache.Rebind(ctx, donorRepo, git.RebindOptions{Owner: altKey})
			Expect(err).To(MatchError(git.ErrCacheEntry))

			tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main"})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(tree.Release)
			_, err = cache.Rebind(ctx, forkRepo, git.RebindOptions{Owner: altKey})
			Expect(err).To(MatchError(git.ErrCacheBusy))
			Expect(cacheOwner(cache, forkRepo)).To(Equal(donorKey))
		})
	})
})
