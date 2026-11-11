package git_test

import (
	"context"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Object read progress", func() {
	It("counts binary, empty and missing requests and emits nothing for a cache hit", func(ctx SpecContext) {
		fixture := newBareRepo("read-progress")
		empty := fixture.commitOn(fixtureCommit{Branch: "main", Path: "empty", Content: "", Message: "empty"})
		sha := fixture.commitOn(fixtureCommit{Branch: "main", Path: "binary", Content: "\x00abc", Message: "binary"})
		repo := newRepository(fixture.FileURL(), git.Auth{})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		var events []git.ReadProgress
		ctxWithObserver := git.WithReadObserver(ctx, func(_ context.Context, event git.ReadProgress) { events = append(events, event) })
		requests := []git.BlobRequest{{SHA: sha, Path: "binary"}, {SHA: sha, Path: "absent"}, {SHA: empty, Path: "empty"}}
		_, err := cache.ReadBlobs(ctxWithObserver, repo, requests)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).NotTo(BeEmpty())
		last := events[len(events)-1]
		Expect([]any{last.Current, last.Total, last.Bytes, last.Done, last.Err}).To(Equal([]any{int64(3), int64(3), int64(4), true, nil}))
		Expect(last.Repository).To(Equal(repo.URL.Redacted()))
		events = nil
		_, err = cache.ReadBlobs(ctxWithObserver, repo, requests)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(BeEmpty())
	})
	It("keeps streamed tree totals unknown and reports cancellation without completing another object", func(ctx SpecContext) {
		fixture := newBareRepo("stream-progress")
		var events []git.ReadProgress
		active, cancel := context.WithCancel(git.WithReadObserver(ctx, func(_ context.Context, event git.ReadProgress) { events = append(events, event) }))
		defer cancel()
		tree, err := (&git.Cache{Root: GinkgoT().TempDir()}).OpenTree(active, git.TreeOptions{Directory: fixture.Path, Revision: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Close)
		_, err = tree.ReadFile("README.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(events[len(events)-1].Total).To(BeZero())
		cancel()
		_, err = tree.ReadFile("README.md")
		Expect(err).To(MatchError(context.Canceled))
		Expect(tree.Close()).To(Succeed())
		last := events[len(events)-1]
		Expect(last.Done).To(BeTrue())
		Expect(last.Current).To(Equal(int64(1)))
		Expect(last.Err).To(MatchError(context.Canceled))
	})
})
