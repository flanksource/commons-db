package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// storeFixture is a remote served over smart HTTP plus a durable store
// directory standing in for the shared (EFS) tier.
type storeFixture struct {
	origin bareRepo
	remote *httpRemote
	repo   *git.Repository
	path   string
}

func newStoreFixture() storeFixture {
	GinkgoHelper()
	origin := newBareRepo("origin")
	remote := newHTTPRemote(origin.Root, false)
	return storeFixture{origin: origin, remote: remote, path: fixtureTempDir(),
		repo: newRepository(remote.URL(origin), git.Auth{Username: remote.User, Password: remote.Password})}
}

func (f storeFixture) cache(store git.Store) *git.Cache {
	return &git.Cache{Root: fixtureTempDir(), Store: store}
}

// commitOnMain pushes one more commit onto origin's main and returns it.
func (f storeFixture) commitOnMain(content string) string {
	GinkgoHelper()
	work := filepath.Join(f.origin.Root, "work")
	gitFixture(work, "checkout", "-q", "main")
	writeFixtureFile(work, "README.md", content)
	gitFixture(work, "commit", "-q", "-am", content)
	gitFixture(work, "push", "-q", f.origin.Path, "main")
	return gitFixture(work, "rev-parse", "HEAD")
}

func branchRef(name string) git.Ref {
	return git.Ref{Name: name, FullName: "refs/heads/" + name, Kind: git.RefBranch}
}

func shaRef(sha string) git.Ref {
	return git.Ref{Name: sha, FullName: sha, SHA: sha, Kind: git.RefSHA}
}

func readWorktreeFile(worktree *git.Worktree, name string) string {
	GinkgoHelper()
	content, err := os.ReadFile(filepath.Join(worktree.Dir, name))
	Expect(err).ToNot(HaveOccurred())
	return string(content)
}

var _ = Describe("Cache.Fetch", func() {
	It("fetches a branch, a tag and a bare SHA in one negotiation", func(ctx SpecContext) {
		f := newStoreFixture()
		Expect(f.cache(nil).Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		single := f.remote.uploadPacks()

		cache := f.cache(nil)
		tag := git.Ref{Name: "v1", FullName: "refs/tags/v1", Kind: git.RefTag}
		Expect(cache.Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{branchRef("main"), tag, shaRef(f.origin.Feature)}})).To(Succeed())
		Expect(f.remote.uploadPacks()).To(Equal(2*single), "three refs cost the upload-pack requests of one")

		for _, sha := range []string{f.origin.Main, f.origin.First, f.origin.Feature} {
			_, err := cache.Files(ctx, f.repo, sha)
			Expect(err).ToNot(HaveOccurred(), sha)
		}
		Expect(f.remote.uploadPacks()).To(Equal(2*single), "every fetched commit is read from the mirror")
	})

	It("refuses to publish without a store", func(ctx SpecContext) {
		f := newStoreFixture()
		err := f.cache(nil).Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}, Publish: true})
		Expect(err).To(MatchError(ContainSubstring("no store")))
		Expect(f.remote.uploadPacks()).To(BeZero())
	})
})

var _ = DescribeTable("Cache.Store",
	func(ctx SpecContext, newStore func(path string) git.Store) {
		f := newStoreFixture()
		store := newStore(f.path)
		Expect(f.cache(store).Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{branchRef("main"), shaRef(f.origin.Feature)}, Publish: true})).To(Succeed())
		f.remote.Server.Close()

		worktree, err := f.cache(store).Checkout(ctx, f.repo, git.CheckoutOptions{Ref: f.origin.Feature})
		Expect(err).ToNot(HaveOccurred(), "a fresh cache root hydrates from the store without the remote")
		DeferCleanup(worktree.Release)
		Expect(readWorktreeFile(worktree, "feature.txt")).To(Equal("feature\n"))

		entries, err := store.Inventory(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(entries).To(HaveExactElements(And(HaveField("Key", f.repo.URL.CacheKey()),
			HaveField("SizeBytes", BeNumerically(">", 0)), HaveField("Published", Not(BeNil())))))
	},
	Entry("mirror", func(path string) git.Store { return &git.MirrorStore{Path: path} }),
	Entry("packfiles", func(path string) git.Store { return &git.PackStore{Path: path, CompactAbove: 8} }),
)

var _ = Describe("PackStore", func() {
	It("appends one bundle per publish and Maintain compacts them past the threshold", func(ctx SpecContext) {
		f := newStoreFixture()
		store := &git.PackStore{Path: f.path, CompactAbove: 2}
		publisher := f.cache(store)
		tips := []string{}
		for _, content := range []string{"third\n", "fourth\n", "fifth\n"} {
			tips = append(tips, f.commitOnMain(content))
			Expect(publisher.Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}, Publish: true})).To(Succeed())
		}
		Expect(bundles(store, f.repo)).To(HaveLen(3))

		Expect(publisher.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(Succeed())
		Expect(bundles(store, f.repo)).To(HaveLen(1))

		f.remote.Server.Close()
		worktree, err := f.cache(store).Checkout(ctx, f.repo, git.CheckoutOptions{Ref: tips[2]})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(worktree.Release)
		Expect(readWorktreeFile(worktree, "README.md")).To(Equal("fifth\n"))
	})

	It("keeps every bundle of concurrent publishers and leaves no temporary file", func(ctx SpecContext) {
		f := newStoreFixture()
		store := &git.PackStore{Path: f.path, CompactAbove: 8}
		var wait sync.WaitGroup
		errs := make([]error, 2)
		for i, ref := range []git.Ref{branchRef("main"), branchRef("feature")} {
			wait.Go(func() {
				errs[i] = f.cache(store).Fetch(ctx, f.repo, git.FetchOptions{Refs: []git.Ref{ref}, Publish: true})
			})
		}
		wait.Wait()
		Expect(errs).To(HaveEach(Succeed()))
		Expect(bundles(store, f.repo)).To(HaveLen(2))

		f.remote.Server.Close()
		reader := f.cache(store)
		for _, sha := range []string{f.origin.Main, f.origin.Feature} {
			_, err := reader.Files(ctx, f.repo, sha)
			Expect(err).ToNot(HaveOccurred(), sha)
		}
	})
})

// bundles lists every file in the repository's pack directory, so a
// leftover temporary file fails the length check.
func bundles(store *git.PackStore, repo *git.Repository) []string {
	GinkgoHelper()
	entries, err := os.ReadDir(filepath.Join(store.Path, "v2", repo.URL.CacheKey(), "packs"))
	Expect(err).ToNot(HaveOccurred())
	names := []string{}
	for _, entry := range entries {
		Expect(strings.HasSuffix(entry.Name(), ".bundle")).To(BeTrue(), entry.Name())
		names = append(names, entry.Name())
	}
	return names
}
