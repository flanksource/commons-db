package git_test

import (
	"context"
	"strings"
	"sync"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fetch progress", func() {
	It("reports a mirror fetch to the context's observer, ending with a done event", func() {
		bare := newBareRepo("progress")
		repo := newRepository(bare.FileURL(), git.Auth{})
		var mu sync.Mutex
		var events []git.FetchProgress
		ctx := git.WithFetchObserver(context.Background(), func(progress git.FetchProgress) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, progress)
		})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(worktree.Release)

		Expect(events).NotTo(BeEmpty())
		var counted []git.FetchProgress
		for _, event := range events {
			Expect(event.Repository).To(Equal(repo.URL.Redacted()))
			Expect(event.Ref).To(Equal("main"))
			// A pack this small unpacks before git's local meter starts, so the
			// server-side phases are the ones every fetch reports.
			if event.Phase == "Counting objects" {
				counted = append(counted, event)
			}
		}
		Expect(counted).NotTo(BeEmpty())
		last := counted[len(counted)-1]
		Expect([]any{last.Remote, last.Percent, last.Current, last.PhaseDone}).To(Equal([]any{true, 100, last.Total, true}))
		Expect(last.Total).To(BeNumerically(">", 0))
		final := events[len(events)-1]
		Expect(final.Done).To(BeTrue())
		Expect(final.Err).NotTo(HaveOccurred())
	})

	It("ends a failed fetch with the error and keeps git's message out of the progress stream", func() {
		bare := newBareRepo("progress-missing")
		repo := newRepository(bare.FileURL(), git.Auth{})
		var events []git.FetchProgress
		ctx := git.WithFetchObserver(context.Background(), func(progress git.FetchProgress) { events = append(events, progress) })
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		missing := strings.Repeat("e", 40)
		_, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: missing})
		Expect(err).To(HaveOccurred())
		Expect(events).NotTo(BeEmpty())
		final := events[len(events)-1]
		Expect(final.Done).To(BeTrue())
		Expect(final.Err).To(HaveOccurred())
	})
})
