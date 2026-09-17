package query_test

import (
	stdcontext "context"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A capture's event count and its events ref describe the same rows, so a host
// that reports only the ref — a span that never ticks progress — must not leave
// the count behind it.
var _ = Describe("a tracked session's event count", func() {
	const refTotal = int64(2)
	var (
		store *fakeSessionStore
		reg   *query.SessionRegistry
	)

	BeforeEach(func() {
		store = newFakeSessionStore()
		reg = query.NewSessionRegistry(query.RegistryOptions{Store: store})
	})

	eventsRef := func(total int64) *query.EventsRef {
		return &query.EventsRef{
			Stream: "span-1", Kind: "sql_xevent", Generation: "gen-1", Low: 1, High: total, From: 1, To: total, Total: total,
			Store: query.EventsStoreLocation{Backend: "sqlite", Host: "spec", File: "records.sqlite"},
		}
	}

	It("is the ref's total when a capture finishes with an events ref and never reported progress", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "span"})).To(Succeed())

		session.Finish(query.FinishUpdate{Events: eventsRef(refTotal)})

		final := store.status(session.ID())
		Expect([]any{final.State, final.EventCount, final.Events.Total}).To(Equal([]any{query.SessionCompleted, refTotal, refTotal}))
	})

	It("is the ref's total when a capture arms with an events ref", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())

		Expect(session.Running(query.RunningUpdate{Handle: "span", Events: eventsRef(refTotal)})).To(Succeed())

		Expect(session.Snapshot().EventCount).To(Equal(refTotal))
	})

	It("is the ref's total when progress reports a ref and no count", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "span"})).To(Succeed())
		DeferCleanup(session.Finish, query.FinishUpdate{})

		session.Progress(query.ProgressUpdate{Events: eventsRef(refTotal)})

		Expect(session.Snapshot().EventCount).To(Equal(refTotal))
	})

	It("keeps the count progress gave explicitly", func() {
		const explicit = int64(5)
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "span"})).To(Succeed())
		DeferCleanup(session.Finish, query.FinishUpdate{})

		session.Progress(query.ProgressUpdate{EventCount: explicit, Events: eventsRef(refTotal)})

		Expect(session.Snapshot().EventCount).To(Equal(explicit))
	})

	It("keeps the last count when a capture finishes without an events ref", func() {
		const counted = int64(3)
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "span"})).To(Succeed())
		session.Progress(query.ProgressUpdate{EventCount: counted})

		session.Finish(query.FinishUpdate{})

		Expect(store.status(session.ID()).EventCount).To(Equal(counted))
	})
})
