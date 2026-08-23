package query_test

import (
	gocontext "context"
	"time"

	context "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The metadata caches are package-level and shared by every request, so what is
// under test is that arming a *request* is what makes its lookups reportable —
// not a switch someone has to remember to turn off again.
var _ = Describe("Recorder inspection observation", func() {
	memo := func() *inspection.Memo[string] {
		return inspection.NewMemo(inspection.MemoOptions[string]{
			Policy: inspection.CachePolicy{
				Name: "opensearch-fields", InitialFreshFor: time.Hour, MaximumFreshFor: time.Hour,
				FillTimeout: time.Second, MaxEntries: 4, MaxWeight: 4,
			},
		})
	}
	load := func(gocontext.Context) (string, error) { return "@timestamp,message", nil }

	It("files the lookups an armed request made, with the miss and the hit told apart", func(ctx SpecContext) {
		recorder := query.NewRecorder(query.RecorderOptions{ID: "inspection-record", Level: logger.Debug})
		armed := query.WithRecorder(context.NewContext(ctx), recorder)
		cache := memo()

		_, err := cache.Get(armed, inspection.GetOptions[string]{Key: "fields:logs-*", Load: load})
		Expect(err).ToNot(HaveOccurred())
		_, err = cache.Get(armed, inspection.GetOptions[string]{Key: "fields:logs-*", Load: load})
		Expect(err).ToNot(HaveOccurred())

		records := recorder.Detail().Inspections
		Expect(records).To(HaveLen(2))
		Expect(records[0].Policy).To(Equal("opensearch-fields"))
		Expect(records[0].Key).To(Equal("fields:logs-*"))
		Expect(records[0].Cached).To(BeFalse())
		Expect(records[1].Cached).To(BeTrue())
		Expect(recorder.Summary().Counts.Inspections).To(Equal(2))
	})

	It("leaves an unarmed request's lookups unobserved", func(ctx SpecContext) {
		unarmed := query.WithRecorder(context.NewContext(ctx), nil)

		_, err := memo().Get(unarmed, inspection.GetOptions[string]{Key: "fields:logs-*", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(inspection.ObserverFrom(unarmed)).To(BeNil())
	})

	// "Re-run inspection" is one request paying to rebuild what it reads. It has
	// to reach every lookup — the whole point is that the caller does not know
	// which of them is holding the stale answer.
	It("rebuilds every lookup for a request that asked to refresh", func(ctx SpecContext) {
		var loads int
		counting := func(gocontext.Context) (string, error) {
			loads++
			return "@timestamp,message", nil
		}
		cache := memo()
		warm := query.WithRecorder(context.NewContext(ctx), query.NewRecorder(query.RecorderOptions{
			ID: "warm", Level: logger.Debug,
		}))
		_, err := cache.Get(warm, inspection.GetOptions[string]{Key: "fields:logs-*", Load: counting})
		Expect(err).ToNot(HaveOccurred())
		Expect(loads).To(Equal(1))

		refreshing := query.WithRecorder(context.NewContext(ctx), query.NewRecorder(query.RecorderOptions{
			ID: "refreshing", Level: logger.Debug, RefreshInspection: true,
		}))
		result, err := cache.Get(refreshing, inspection.GetOptions[string]{Key: "fields:logs-*", Load: counting})

		Expect(err).ToNot(HaveOccurred())
		Expect(loads).To(Equal(2))
		Expect(result.Cache.Cached).To(BeFalse())
	})

	// It costs the caller who asked and nobody else — which is the difference
	// between this and flushing the cache.
	It("leaves the next ordinary request reading the cache", func(ctx SpecContext) {
		var loads int
		counting := func(gocontext.Context) (string, error) {
			loads++
			return "@timestamp,message", nil
		}
		cache := memo()
		refreshing := query.WithRecorder(context.NewContext(ctx), query.NewRecorder(query.RecorderOptions{
			ID: "refreshing", Level: logger.Debug, RefreshInspection: true,
		}))
		_, err := cache.Get(refreshing, inspection.GetOptions[string]{Key: "fields:logs-*", Load: counting})
		Expect(err).ToNot(HaveOccurred())

		ordinary, err := cache.Get(context.NewContext(ctx), inspection.GetOptions[string]{
			Key: "fields:logs-*", Load: counting,
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(loads).To(Equal(1))
		Expect(ordinary.Cache.Cached).To(BeTrue())
	})
})
