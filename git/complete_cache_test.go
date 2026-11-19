package git_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// mirrorCommits lists every commit object the repository at dir can read.
func mirrorCommits(dir string) []string {
	GinkgoHelper()
	var commits []string
	for _, line := range strings.Split(gitFixture(dir, "cat-file", "--batch-all-objects", "--batch-check=%(objecttype) %(objectname)"), "\n") {
		if sha, ok := strings.CutPrefix(line, "commit "); ok {
			commits = append(commits, sha)
		}
	}
	return commits
}

// localObjects is how many objects the repository at dir stores itself,
// loose or packed, leaving out what it borrows from alternates.
func localObjects(dir string) map[string]string {
	GinkgoHelper()
	counts := map[string]string{}
	for _, line := range strings.Split(gitFixture(dir, "count-objects", "-v"), "\n") {
		if key, value, ok := strings.Cut(line, ": "); ok && (key == "count" || key == "in-pack") {
			counts[key] = value
		}
	}
	return counts
}

func shas(commits []git.Commit) []string {
	result := make([]string, len(commits))
	for i, commit := range commits {
		result[i] = commit.SHA
	}
	return result
}

func mirrorOf(cache *git.Cache, repo *git.Repository) string {
	return filepath.Join(cache.Root, "v2", repo.URL.CacheKey(), "mirror.git")
}

var _ = Describe("complete histories in the git cache", func() {
	DescribeTable("fetches complete history for a tree read, so history reads need no further fetch", func(ctx SpecContext, pooled bool) {
		donor, _, remote := organizationFixtures()
		repo := newRepository(remote.URL(donor), git.Auth{Username: remote.User, Password: remote.Password})
		if pooled {
			repo = organizationRepository(remote, donor, "example")
		}
		cache := &git.Cache{Root: fixtureTempDir()}
		_, err := cache.Changes(ctx, git.ChangesOptions{Repository: repo, Base: donor.Main, Head: donor.Feature})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(mirrorOf(cache, repo), "rev-parse", "--is-shallow-repository")).To(Equal("false"))
		remote.Server.Close()

		base, err := cache.MergeBase(ctx, repo, git.MergeBaseOptions{Base: donor.Main, Head: donor.Feature})
		Expect(err).NotTo(HaveOccurred())
		Expect(base).To(Equal(donor.Main))
		logged, err := cache.Log(ctx, repo, git.LogOptions{Base: donor.Main, Head: donor.Feature})
		Expect(err).NotTo(HaveOccurred())
		Expect(shas(logged)).To(Equal([]string{donor.Feature}))
		history, err := cache.History(ctx, repo, donor.Feature, git.HistoryOptions{Detail: git.HistoryMetadata})
		Expect(err).NotTo(HaveOccurred())
		Expect(shas(history)).To(Equal([]string{donor.Feature, donor.Main, donor.First}))
	},
		Entry("in a private mirror", false),
		Entry("through the organization pool", true),
	)

	DescribeTable("keeps no object in a pooled mirror that its pool holds", func(ctx SpecContext, fill func(context.Context, *git.Cache, *git.Repository, bareRepo)) {
		donor, _, remote := organizationFixtures()
		repo := organizationRepository(remote, donor, "example")
		cache := &git.Cache{Root: fixtureTempDir()}
		fill(ctx, cache, repo, donor)
		Expect(localObjects(mirrorOf(cache, repo))).To(Equal(map[string]string{"count": "0", "in-pack": "0"}))
		Expect(mirrorCommits(mirrorOf(cache, repo))).To(ContainElements(donor.First, donor.Main), "the pool serves the history")
	},
		Entry("after a checkout fetched into the pool", func(ctx context.Context, cache *git.Cache, repo *git.Repository, donor bareRepo) {
			tree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(tree.Release()).To(Succeed())
		}),
		Entry("after a file read fetched into the pool", func(ctx context.Context, cache *git.Cache, repo *git.Repository, donor bareRepo) {
			_, err := cache.Show(ctx, repo, donor.Main, "README.md")
			Expect(err).NotTo(HaveOccurred())
		}),
		Entry("after a prefetch retained into the pool", func(ctx context.Context, cache *git.Cache, repo *git.Repository, donor bareRepo) {
			Expect(cache.Fetch(ctx, repo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		}),
	)
})

var _ = Describe("legacy git cache entries", func() {
	const legacyURL = "https://github.com/example/legacy-donor.git"
	var (
		cache *git.Cache
		donor bareRepo
		entry string
		key   string
	)
	writeEntry := func(dir string, binding map[string]any, files ...string) {
		GinkgoHelper()
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		if binding != nil {
			data, err := json.Marshal(binding)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(dir, "repository.json"), data, 0o600)).To(Succeed())
		}
		for _, file := range files {
			Expect(os.WriteFile(filepath.Join(dir, file), nil, 0o600)).To(Succeed())
		}
	}
	BeforeEach(func() {
		donor = newBareRepo("legacy-donor")
		cache = &git.Cache{Root: fixtureTempDir()}
		parsed, err := git.ParseRepoURL(legacyURL, "")
		Expect(err).NotTo(HaveOccurred())
		key = parsed.CacheKey()
		entry = filepath.Join(cache.Root, key)
		gitFixture(cache.Root, "clone", "-q", "--mirror", donor.Path, filepath.Join(entry, "mirror.git"))
		writeEntry(entry, map[string]any{"version": 1, "owner": key, "repository": legacyURL}, "mirror.lock", "repository.lock")
		Expect(os.MkdirAll(filepath.Join(entry, "worktrees"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(entry, "pins"), 0o755)).To(Succeed())
	})

	It("imports a legacy mirror into the pool and removes only entries matching the legacy layout", func(ctx SpecContext) {
		bound, err := git.ParseRepoURL("https://github.com/example/bound.git", "")
		Expect(err).NotTo(HaveOccurred())
		boundEntry := filepath.Join(cache.Root, bound.CacheKey())
		writeEntry(boundEntry, map[string]any{"version": 1, "owner": key, "repository": "https://github.com/example/bound.git"}, "repository.lock")
		unknown := filepath.Join(cache.Root, "unrelated-tool")
		writeEntry(unknown, nil, "notes.txt")
		almost := filepath.Join(cache.Root, "almost-legacy")
		writeEntry(almost, map[string]any{"version": 1, "owner": key, "repository": legacyURL}, "repository.lock", "keep.me")

		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(Succeed())

		Expect(entry).NotTo(BeADirectory())
		Expect(boundEntry).NotTo(BeADirectory())
		Expect(filepath.Join(unknown, "notes.txt")).To(BeAnExistingFile())
		Expect(filepath.Join(almost, "keep.me")).To(BeAnExistingFile())
		pools, err := filepath.Glob(filepath.Join(cache.Root, "v2", "pools", "*", "pool.git"))
		Expect(err).NotTo(HaveOccurred())
		Expect(pools).To(HaveLen(1))
		Expect(gitFixture(pools[0], "rev-parse", "refs/commons-pool/tips/"+key+"/legacy/heads/feature")).To(Equal(donor.Feature))
		Expect(mirrorCommits(pools[0])).To(ContainElements(donor.First, donor.Main, donor.Feature))
	})

	It("fails loudly and keeps a legacy entry whose import fails", func(ctx SpecContext) {
		objects := filepath.Join(entry, "mirror.git", "objects")
		Expect(os.RemoveAll(objects)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(objects, "info"), 0o755)).To(Succeed())

		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Hour})).To(MatchError(ContainSubstring("migrate legacy git cache " + entry)))

		Expect(filepath.Join(entry, "repository.json")).To(BeAnExistingFile())
		Expect(filepath.Join(entry, "mirror.git", "HEAD")).To(BeAnExistingFile())
	})
})
