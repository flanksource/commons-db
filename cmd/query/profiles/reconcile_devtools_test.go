package profiles

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
)

var _ = Describe("Reconcile debug capture", func() {
	DescribeTable("records both profile queries",
		func(source, dest query.Profile, flags ReconcileFlags, wantMode query.ReconcileMode) {
			query.RegisterProvider(rowsProvider{name: source.Provider.Type, rows: []query.Row{{"id": "A"}}})
			query.RegisterProvider(rowsProvider{name: dest.Provider.Type, rows: []query.Row{{"id": "A"}}})

			store, err := NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			Expect(store.Save(context.Background(), source)).To(Succeed())
			Expect(store.Save(context.Background(), dest)).To(Succeed())
			service, err := New(Options{
				Store:      func() (Store, error) { return store, nil },
				Context:    func() dbcontext.Context { return dbcontext.New() },
				DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			})
			Expect(err).NotTo(HaveOccurred())

			capture := query.NewRecorder(query.RecorderOptions{ID: "reconcile-capture", Level: logger.Debug})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+source.Name+"/reconcile", nil)
			request = devtools.RequestWithRecorder(request, capture)
			result, err := service.Reconcile(rpc.ContextWithRequest(request.Context(), request), source.Name, flags)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Mode).To(Equal(wantMode))

			Expect(capture.Summary().Operations).To(ConsistOf(
				gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{"Provider": Equal(source.Provider.Type), "Query": Equal(source.Query)}),
				gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{"Provider": Equal(dest.Provider.Type), "Query": Equal(dest.Query)}),
			))
			Expect(capture.Detail().Operations).To(HaveLen(2))
		},
		Entry("for a buffered join",
			query.Profile{Name: "debug-buffered-source", Provider: query.ProviderConfig{Type: "debug-buffered-source"}, Query: "select id from emitted"},
			query.Profile{Name: "debug-buffered-dest", Provider: query.ProviderConfig{Type: "debug-buffered-dest"}, Query: "select id from ingested"},
			ReconcileFlags{Dest: "debug-buffered-dest", KeyCEL: "row.id"},
			query.ReconcileBuffered,
		),
		Entry("for a merge join",
			query.Profile{Name: "debug-merged-source", Provider: query.ProviderConfig{Type: "debug-merged-source"}, Query: "select id from emitted order by id", Order: query.Order{{Column: "id", Unique: true}}},
			query.Profile{Name: "debug-merged-dest", Provider: query.ProviderConfig{Type: "debug-merged-dest"}, Query: "select id from ingested order by id", Order: query.Order{{Column: "id", Unique: true}}},
			ReconcileFlags{Dest: "debug-merged-dest", KeyColumns: []string{"id"}},
			query.ReconcileMerged,
		),
	)
})
