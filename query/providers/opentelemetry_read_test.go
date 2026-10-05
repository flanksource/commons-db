// Specs for reading span documents of an opentelemetry connection: an import of
// a window that ends once caught up, and a follow that keeps reading.

package providers_test

import (
	gocontext "context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/types"
)

var _ = Describe("ReadOpenTelemetrySpans", func() {
	var stub *openSearchTailStub
	var ctx dbcontext.Context
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		stub = newOpenSearchTailStub(
			tailDoc{id: "a", at: at, message: "first"},
			tailDoc{id: "b", at: at.Add(time.Second), message: "second"},
			tailDoc{id: "c", at: at.Add(2 * time.Second), message: "third"},
		)
		DeferCleanup(stub.server.Close)
		ctx = dbcontext.New().WithConnectionResolver(func(_ dbcontext.Context, reference string) (*models.Connection, error) {
			switch reference {
			case "connection://traces":
				return &models.Connection{Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
					Properties: types.JSONStringMap{"connection": "connection://spans"}}, nil
			case "connection://spans":
				return &models.Connection{Name: "spans", Type: models.ConnectionTypeOpenSearch, URL: stub.server.URL}, nil
			}
			return nil, nil
		})
	})

	It("imports the spans of a window, oldest first, each naming the document it came from", func() {
		var rows []query.Row
		Expect(providers.ReadOpenTelemetrySpans(ctx, providers.OpenTelemetryRead{
			Connection: "connection://traces", Options: map[string]any{"index": "logs"},
			From: "2026-10-05T08:00:00Z", To: "2026-10-05T10:00:00Z",
		}, func(row query.Row) { rows = append(rows, row) })).To(Succeed())

		Expect(rows).To(HaveLen(3))
		Expect(rows[0]["source_index"]).To(Equal("logs"))
		Expect(rows[0]["source_id"]).To(Equal("a"))
		Expect(rows[2]["source_id"]).To(Equal("c"))
		bounds := openSearchRangeBounds(stub.lastBody(), "@timestamp")
		Expect(bounds).To(HaveKey("gte"))
		Expect(bounds).To(HaveKey("lte"))
	})

	It("follows the index, reading the spans indexed after it caught up, until stopped", func() {
		followCtx, cancel := gocontext.WithCancel(ctx)
		defer cancel()
		received := make(chan query.Row, 10)
		done := make(chan error, 1)
		go func() {
			done <- providers.ReadOpenTelemetrySpans(ctx.Wrap(followCtx), providers.OpenTelemetryRead{
				Connection: "connection://traces", Options: map[string]any{"index": "logs"},
				Follow: true, Poll: 20 * time.Millisecond,
			}, func(row query.Row) { received <- row })
		}()
		for range 3 {
			Eventually(received).Should(Receive())
		}
		stub.write(tailDoc{id: "d", at: at.Add(3 * time.Second), message: "fourth"})
		var row query.Row
		Eventually(received).Should(Receive(&row))
		Expect(row["source_id"]).To(Equal("d"))
		cancel()
		Eventually(done).Should(Receive(BeNil()))
	})

	It("refuses a followed read with an end", func() {
		err := providers.ReadOpenTelemetrySpans(ctx, providers.OpenTelemetryRead{
			Connection: "connection://traces", Follow: true, To: "now",
		}, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring("follow")))
	})
})
