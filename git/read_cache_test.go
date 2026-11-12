package git_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("persistent SHA read cache", func() {
	It("isolates history options, caller security namespaces and repository credentials", func(ctx SpecContext) {
		fixture := newBareRepo("isolated-cache")
		repo := newRepository(fixture.FileURL(), git.Auth{})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		record := argvRecorder()
		read := func(ctx context.Context, limit int) []byte {
			_, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{Limit: limit})
			Expect(err).NotTo(HaveOccurred())
			commands, err := os.ReadFile(record)
			Expect(err).NotTo(HaveOccurred())
			return commands
		}
		caller := git.WithSecurityNamespace(ctx, "tenant-one")
		before := read(caller, 1)
		Expect(read(caller, 1)).To(Equal(before))
		changedOptions := read(caller, 2)
		Expect(changedOptions).NotTo(Equal(before))
		otherCaller := read(git.WithSecurityNamespace(ctx, "tenant-two"), 1)
		Expect(otherCaller).NotTo(Equal(changedOptions))
		repo.SecurityNamespace = "rotated-credentials"
		Expect(read(caller, 1)).NotTo(Equal(otherCaller))
	})

	It("performs zero further Git commands for warm pinned history and comparison reads", func() {
		fixture := newBareRepo("history-cache")
		repo := newRepository(fixture.FileURL(), git.Auth{})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		record := argvRecorder()
		queries := []func() (any, error){
			func() (any, error) { return cache.Files(GinkgoT().Context(), repo, fixture.Main) },
			func() (any, error) {
				return cache.History(GinkgoT().Context(), repo, fixture.Main, git.HistoryOptions{Limit: 1})
			},
			func() (any, error) {
				return cache.Log(GinkgoT().Context(), repo, git.LogOptions{Base: fixture.First, Head: fixture.Main})
			},
			func() (any, error) { return cache.Compare(GinkgoT().Context(), repo, fixture.First, fixture.Main) },
			func() (any, error) {
				return cache.MergeBase(GinkgoT().Context(), repo, git.MergeBaseOptions{Base: fixture.First, Head: fixture.Main})
			},
			func() (any, error) {
				return cache.Changes(GinkgoT().Context(), git.ChangesOptions{Repository: repo, Base: fixture.First, Head: fixture.Main})
			},
		}
		originals := make([][]byte, len(queries))
		for index, query := range queries {
			value, err := query()
			Expect(err).NotTo(HaveOccurred())
			originals[index], err = json.Marshal(value)
			Expect(err).NotTo(HaveOccurred())
		}
		before, err := os.ReadFile(record)
		Expect(err).NotTo(HaveOccurred())
		for index, query := range queries {
			value, err := query()
			Expect(err).NotTo(HaveOccurred())
			raw, err := json.Marshal(value)
			Expect(err).NotTo(HaveOccurred())
			Expect(raw).To(Equal(originals[index]))
		}
		after, err := os.ReadFile(record)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
	})
	It("serves immutable bytes through a fresh cache after the source becomes unavailable", func() {
		fixture := newBareRepo("immutable")
		repo := newRepository(fixture.FileURL(), git.Auth{})
		root := GinkgoT().TempDir()
		cache := &git.Cache{Root: root}
		requests := []git.BlobRequest{{SHA: fixture.Main, Path: "README.md"}}
		first, err := cache.ReadBlobs(context.Background(), repo, requests)
		Expect(err).NotTo(HaveOccurred())
		first[0].Content[0] = 'X'
		Expect(os.Rename(fixture.Path, filepath.Join(fixture.Root, "unavailable"))).To(Succeed())
		cache = &git.Cache{Root: root}
		second, err := cache.ReadBlobs(context.Background(), repo, requests)
		Expect(err).NotTo(HaveOccurred())
		Expect(second[0].Content).To(Equal([]byte("second\n")))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = cache.ReadBlobs(ctx, repo, requests)
		Expect(err).To(MatchError(context.Canceled))
	})
})
