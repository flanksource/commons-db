package recordresults_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing/sqltrace"
)

var _ = Describe("TraceStore", func() {
	ctx := context.Background()

	It("commits a capture's rows and describes them as the results store does", func() {
		results, _ := openLocal(recordstore.BackendSQLite)
		var store sqltrace.RecordStore
		store, err := recordresults.NewTraceStore(results)
		Expect(err).ToNot(HaveOccurred())

		var rows []recordstore.Row
		for _, event := range sampleEvents(1, 3) {
			row, err := recordstore.EncodeRow(event)
			Expect(err).ToNot(HaveOccurred())
			rows = append(rows, row)
		}
		appended, err := store.Append(ctx, "run-1", "sample_event", rows)
		Expect(err).ToNot(HaveOccurred())
		Expect(appended.Window.Len()).To(Equal(int64(3)))
		Expect(store.Seal(ctx, "run-1")).To(Succeed())

		ref, err := store.EventsRef(ctx, "run-1", 2, 3)
		Expect(err).ToNot(HaveOccurred())
		want, err := results.Ref(ctx, "run-1", 2, 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(ref).To(Equal(*want.EventsRef()))

		sealed, err := results.Backend.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(sealed.Sealed).To(BeTrue())
	})

	It("refuses a results store that is not open", func() {
		_, err := recordresults.NewTraceStore(nil)

		Expect(err).To(MatchError(ContainSubstring("not open")))
	})
})
