package providers_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("opensearch diagnostics", func() {
	It("records the search body it sent, the endpoint it went to and what came back", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		profile := openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{
				"timeField": "@timestamp",
				"query":     map[string]any{"op": "term", "field": "service", "value": "api"},
			},
		})
		profile.Params = []query.ParamDef{{Name: "since", Role: query.ParamRoleTimeFrom}}

		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "opensearch", Query: profile.Query, Options: profile.Provider.Options, Detail: query.DiagnosticFull})
		ctx := query.WithDiagnosticSink(context.New(), diagnostics)
		_, err := query.Execute(ctx, profile, map[string]any{"since": "now-6h"})
		Expect(err).ToNot(HaveOccurred())

		snapshot := diagnostics.Snapshot()
		// The range clause exists only at execution time — it is folded onto the
		// time field out of a param the profile declares and the URL supplies —
		// so a debug view that reports the profile's options reports a query
		// nobody ran.
		Expect(snapshot.Request.Query).To(ContainSubstring(`"gte":"now-6h"`))
		Expect(snapshot.Request.Query).To(ContainSubstring(`"service":"api"`))
		Expect(snapshot.Request.Details).To(HaveKeyWithValue("index", "logs-*"))

		Expect(snapshot.Request.Method).To(Equal("POST"))
		Expect(snapshot.Request.URL).To(HavePrefix(server.URL))
		Expect(snapshot.Request.URL).To(ContainSubstring("_search"))
		Expect(snapshot.Request.Headers).To(HaveKeyWithValue("Content-Type", "application/json"))

		Expect(snapshot.Response.Status).To(Equal(200))
		Expect(snapshot.Response.Headers).To(HaveKeyWithValue("Content-Type", "application/json"))
		Expect(snapshot.Response.Preview).To(ContainSubstring("hits"))
		Expect(snapshot.Response.Details).To(HaveKeyWithValue("index", "logs-*"))
	})

	It("leaves an ordinary run unrecorded", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		profile := openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{"query": map[string]any{"op": "match_all"}},
		})
		_, err := query.Execute(context.New(), profile, nil)
		Expect(err).ToNot(HaveOccurred())
	})
})
