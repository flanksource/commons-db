package recordstore_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flanksource/clicky/cache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

// routeKey carries a spec's route on the context, the way a server carries
// the tenant a request runs for.
type routeKey struct{}

func onRoute(route string) context.Context {
	return context.WithValue(context.Background(), routeKey{}, route)
}

func routeOf(ctx context.Context) (string, error) {
	route, _ := ctx.Value(routeKey{}).(string)
	if route == "" {
		return "", errors.New("the context carries no route")
	}
	return route, nil
}

func openMemoryKV() recordstore.Backend {
	backend, err := kv.New(kv.Options{
		Store: cache.NewMemory(), Prefix: "records", Schema: recordstoretest.Schema, TTL: time.Hour, MaxChunkBytes: 1 << 20,
	})
	Expect(err).ToNot(HaveOccurred())
	return backend
}

// countingOpener opens a fresh in-process kv backend per call and remembers
// every backend it handed out, per route.
type countingOpener struct {
	mu     sync.Mutex
	opened map[string][]recordstore.Backend
}

func newCountingOpener() *countingOpener {
	return &countingOpener{opened: map[string][]recordstore.Backend{}}
}

func (o *countingOpener) open(_ context.Context, route string) (recordstore.Backend, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	backend := openMemoryKV()
	o.opened[route] = append(o.opened[route], backend)
	return backend, nil
}

func (o *countingOpener) count(route string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.opened[route])
}

func newRouter(options recordstore.RouterOptions) *recordstore.Router {
	router, err := recordstore.NewRouter(options)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(router.Close)
	return router
}

var _ = Describe("Router over one route", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		store, clock := cache.NewMemory(), &fakeClock{now: time.Now()}
		open := func() recordstore.Backend {
			router, err := recordstore.NewRouter(recordstore.RouterOptions{
				Route: func(context.Context) (string, error) { return "only", nil },
				Open: func(context.Context, string) (recordstore.Backend, error) {
					return kv.New(kv.Options{
						Store: store, Prefix: "records", Schema: recordstoretest.Schema, TTL: recordstoretest.TTL,
						MaxChunkBytes: 1 << 20, Now: clock.Now,
					})
				},
			})
			Expect(err).ToNot(HaveOccurred())
			return router
		}
		return recordstoretest.Harness{
			Backend: open(), Advance: clock.Advance, Now: clock.Now, Reopen: open,
			// The in-process store expires against the wall clock.
			Elapse: func(d time.Duration) {
				clock.Advance(d)
				time.Sleep(d)
			},
		}
	})
})

var _ = Describe("Router", func() {
	It("refuses options without a route or an opener", func() {
		_, err := recordstore.NewRouter(recordstore.RouterOptions{Open: newCountingOpener().open})
		Expect(err).To(MatchError(ContainSubstring("Route")))
		_, err = recordstore.NewRouter(recordstore.RouterOptions{Route: routeOf})
		Expect(err).To(MatchError(ContainSubstring("Open")))
	})

	It("keeps each route's streams apart", func() {
		router := newRouter(recordstore.RouterOptions{Route: routeOf, Open: newCountingOpener().open})
		_, err := router.Append(onRoute("a"), "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())

		meta, err := router.Meta(onRoute("a"), "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Total).To(Equal(int64(3)))

		_, err = router.Meta(onRoute("b"), "run-1")
		Expect(err).To(MatchError(recordstore.ErrNotFound))
		Expect(router.Scan(onRoute("b"), "run-1", 0, func(int64, recordstore.Row) error { return nil })).
			To(MatchError(recordstore.ErrNotFound))
	})

	It("opens each route once, however many calls it serves", func() {
		opener := newCountingOpener()
		router := newRouter(recordstore.RouterOptions{Route: routeOf, Open: opener.open})
		for range 3 {
			_, err := router.Append(onRoute("a"), "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
			Expect(err).ToNot(HaveOccurred())
		}
		Expect(opener.count("a")).To(Equal(1))
	})

	It("forgets a route only while it still holds the backend it was asked to forget", func() {
		opener := newCountingOpener()
		router := newRouter(recordstore.RouterOptions{Route: routeOf, Open: opener.open})
		_, err := router.Append(onRoute("a"), "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		current := opener.opened["a"][0]

		By("ignoring a stale backend, so an old owner cannot evict its replacement")
		Expect(router.Forget("a", openMemoryKV())).To(Succeed())
		Expect(router.Meta(onRoute("a"), "run-1")).To(HaveField("Total", int64(2)))
		Expect(opener.count("a")).To(Equal(1))

		By("dropping the current one, so the next call opens the route afresh")
		Expect(router.Forget("a", current)).To(Succeed())
		_, err = router.Meta(onRoute("a"), "run-1")
		Expect(err).To(MatchError(recordstore.ErrNotFound))
		Expect(opener.count("a")).To(Equal(2))
	})

	It("fails a call whose route cannot be resolved or opened, naming the route", func() {
		router := newRouter(recordstore.RouterOptions{
			Route: routeOf,
			Open: func(_ context.Context, route string) (recordstore.Backend, error) {
				return nil, errors.New("no store for this tenant")
			},
		})
		_, err := router.Meta(context.Background(), "run-1")
		Expect(err).To(MatchError(ContainSubstring("carries no route")))

		_, err = router.Meta(onRoute("a"), "run-1")
		Expect(err).To(MatchError(And(ContainSubstring(`"a"`), ContainSubstring("no store for this tenant"))))
	})

	It("refuses calls once closed", func() {
		router, err := recordstore.NewRouter(recordstore.RouterOptions{Route: routeOf, Open: newCountingOpener().open})
		Expect(err).ToNot(HaveOccurred())
		Expect(router.Close()).To(Succeed())
		_, err = router.Meta(onRoute("a"), "run-1")
		Expect(err).To(MatchError(ContainSubstring("closed")))
	})
})
