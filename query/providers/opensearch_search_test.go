package providers_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// openSearchCapture records what the provider actually sent to the backend. The
// raw bytes are kept alongside the decoded body so a test can assert on the
// exact JSON text where re-encoding would otherwise hide a difference.
type openSearchCapture struct {
	raw  string
	body map[string]any
	size string
}

// stubOpenSearch answers one search with no hits and captures the request.
func stubOpenSearch(capture *openSearchCapture) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		raw, err := io.ReadAll(r.Body)
		Expect(err).ToNot(HaveOccurred())
		capture.raw = string(raw)
		Expect(json.Unmarshal(raw, &capture.body)).To(Succeed())
		capture.size = r.URL.Query().Get("size")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
	}))
}

func openSearchProfile(address string, options map[string]any) query.Profile {
	options["address"] = address
	options["index"] = "logs-*"
	return query.Profile{
		Name:     "logs",
		Provider: query.ProviderConfig{Type: "opensearch", Options: options},
		Columns:  []query.ColumnDef{{Name: "service"}},
	}
}

var _ = Describe("opensearch structured search", func() {
	It("compiles the specification and sends size out of band", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		_, err := query.Execute(context.New(), openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{
				"size": 25,
				"sort": []any{map[string]any{"field": "@timestamp", "order": "desc"}},
				"query": map[string]any{
					"op": "bool",
					"conditions": []any{
						map[string]any{"op": "term", "field": "level", "value": "error"},
						map[string]any{"op": "match", "occur": "must", "field": "message", "value": "timeout"},
					},
				},
			},
		}), nil)
		Expect(err).ToNot(HaveOccurred())

		Expect(capture.size).To(Equal("25"),
			"size must travel as a URL parameter, which the searcher would otherwise override")
		Expect(capture.body).ToNot(HaveKey("size"))
		Expect(capture.body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"level": "error"}}},
			"must":   []any{map[string]any{"match": map[string]any{"message": "timeout"}}},
		}}))
		Expect(capture.body["sort"]).To(Equal([]any{
			map[string]any{"@timestamp": map[string]any{"order": "desc"}},
		}))
	})

	It("binds parameters structurally and folds the time range onto the time field", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		profile := openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{
				"timeField": "@timestamp",
				"query": map[string]any{
					"op": "term", "field": "service", "value": map[string]any{"param": "service"},
				},
			},
		})
		profile.Params = []query.ParamDef{
			{Name: "service", Template: "{value}-api"},
			{Name: "since", Role: query.ParamRoleTimeFrom},
			{Name: "rows", Role: query.ParamRoleLimit},
		}

		_, err := query.Execute(context.New(), profile, map[string]any{
			"service": "prod", "since": "now-6h", "rows": "75",
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(capture.size).To(Equal("75"))
		Expect(capture.body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"term": map[string]any{"service": "prod-api"}},
				map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": "now-6h"}}},
			},
		}}))
	})

	// The Kenya Prod regression: an operand that reaches its param textually was
	// sent to the backend as the template text, and the specification then
	// reported the param as unreferenced.
	DescribeTable("interpolates a param into a specification operand",
		func(country, expected string) {
			var capture openSearchCapture
			server := stubOpenSearch(&capture)
			defer server.Close()

			profile := openSearchProfile(server.URL, map[string]any{
				"search": map[string]any{
					"query": map[string]any{
						"op": "term", "field": "process.serviceName", "value": "{{.params.country}}-api",
					},
				},
			})
			profile.Params = []query.ParamDef{{
				Name: "country", Type: query.ParamTypeEnum,
				Options: []string{"kenya", "botswana"}, Default: "kenya",
			}}

			supplied := map[string]any{}
			if country != "" {
				supplied["country"] = country
			}
			_, err := query.Execute(context.New(), profile, supplied)
			Expect(err).ToNot(HaveOccurred())

			Expect(capture.body["query"]).To(Equal(
				map[string]any{"term": map[string]any{"process.serviceName": expected}}))
		},
		Entry("the default value", "", "kenya-api"),
		Entry("a supplied value", "botswana", "botswana-api"),
	)

	It("composes runtime column filters onto a compiled specification", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		_, err := query.Execute(context.New(), openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{
				"query": map[string]any{"op": "match", "field": "message", "value": "failed"},
			},
		}), map[string]any{"filter.service": "api,!debug"})
		Expect(err).ToNot(HaveOccurred())

		outer := capture.body["query"].(map[string]any)["bool"].(map[string]any)
		Expect(outer["filter"]).To(ConsistOf(
			map[string]any{"match": map[string]any{"message": "failed"}},
			map[string]any{"terms": map[string]any{"service": []any{"api"}}},
		))
		Expect(outer["must_not"]).To(ConsistOf(
			map[string]any{"terms": map[string]any{"service": []any{"debug"}}},
		))
	})

	It("emits a mapped list include once and keeps its native exclusions", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		profile := openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{"query": map[string]any{
				"op": "terms", "field": "scheme.id",
				"value": map[string]any{"param": "schemes"}, "optional": true,
			}},
		})
		profile.Params = []query.ParamDef{{
			Name: "schemes", Type: query.ParamTypeList, Field: "scheme.id",
		}}

		_, err := query.Execute(context.New(), profile, map[string]any{
			"schemes": []string{"one", "two", "!three"},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(capture.body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"terms": map[string]any{"scheme.id": []any{"one", "two"}}},
			},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"scheme.id": []any{"three"}}},
			},
		}}))
	})

	It("accepts a native-only list mapping in a structured search", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		profile := openSearchProfile(server.URL, map[string]any{
			"search": map[string]any{"query": map[string]any{"op": "match_all"}},
		})
		profile.Params = []query.ParamDef{{
			Name: "schemes", Type: query.ParamTypeList, Field: "scheme.id",
		}}

		_, err := query.Execute(context.New(), profile, map[string]any{
			"schemes": []string{"one", "!three"},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(capture.body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"scheme.id": []any{"one"}}},
			},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"scheme.id": []any{"three"}}},
			},
		}}))
	})

	It("uses options.limit as the hit cap when the specification sets no size", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		_, err := query.Execute(context.New(), openSearchProfile(server.URL, map[string]any{
			"limit":  "40",
			"search": map[string]any{"query": map[string]any{"op": "match_all"}},
		}), nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(capture.size).To(Equal("40"))
	})

	DescribeTable("asks for the smaller of the specification's size and the page",
		func(specSize int, expected string, capped bool) {
			var capture openSearchCapture
			server := stubOpenSearch(&capture)
			defer server.Close()

			profile := openSearchProfile(server.URL, map[string]any{
				"search": map[string]any{
					"size":  specSize,
					"query": map[string]any{"op": "match_all"},
				},
			})
			for page, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1000}) {
				Expect(err).ToNot(HaveOccurred())
				// A page held short by the profile's own size says so, so it is
				// not read as the end of the index.
				Expect(page.Truncated).To(Equal(capped))
				break
			}
			Expect(capture.size).To(Equal(expected))
		},
		Entry("a size below the page", 40, "40", true),
		Entry("a size above the page", 5000, "1000", false),
	)

	It("preserves numeric literals in a raw query body", func() {
		var capture openSearchCapture
		server := stubOpenSearch(&capture)
		defer server.Close()

		// Beyond 2^53 a float64 round-trip silently rewrites the literal, which
		// is exactly what an id or byte count must survive.
		const exactID = "9007199254740993"
		profile := openSearchProfile(server.URL, map[string]any{})
		profile.Query = `{"query":{"range":{"event_id":{"gte":` + exactID + `}}}}`

		_, err := query.Execute(context.New(), profile, map[string]any{"filter.service": "api"})
		Expect(err).ToNot(HaveOccurred())

		Expect(capture.raw).To(ContainSubstring(`"gte":`+exactID),
			"composing column filters must not reformat the author's numbers")
	})

	DescribeTable("rejects an ambiguous query source",
		func(options map[string]any, rawQuery, expected string) {
			server := stubOpenSearch(&openSearchCapture{})
			defer server.Close()

			profile := openSearchProfile(server.URL, options)
			profile.Query = rawQuery
			_, err := query.Execute(context.New(), profile, nil)
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("both a specification and a raw query",
			map[string]any{"search": map[string]any{"query": map[string]any{"op": "match_all"}}},
			`{"query":{"match_all":{}}}`,
			"mutually exclusive"),
		Entry("neither", map[string]any{}, "",
			"requires a query or provider.options.search"),
		Entry("an invalid specification",
			map[string]any{"search": map[string]any{"query": map[string]any{"op": "term", "field": "level"}}},
			"",
			`operator "term" requires a value`),
	)
})
