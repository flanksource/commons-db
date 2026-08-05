package esdsl

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func intPtr(v int) *int           { return &v }
func boolPtr(v bool) *bool        { return &v }
func floatPtr(v float64) *float64 { return &v }

// compileQuery compiles a single-condition search and returns its query clause.
func compileQuery(c Condition, params ...ParamBinding) map[string]any {
	GinkgoHelper()
	compiled, err := Compile(CompileRequest{Search: Search{Query: &c}, Params: params})
	Expect(err).ToNot(HaveOccurred())
	query, ok := compiled.Body["query"].(map[string]any)
	Expect(ok).To(BeTrue(), "compiled body has no query clause")
	return query
}

var _ = Describe("Compile leaf operators", func() {
	DescribeTable("renders the OpenSearch clause",
		func(condition Condition, expected map[string]any) {
			Expect(compileQuery(condition)).To(Equal(expected))
		},
		Entry("term", Condition{Op: OpTerm, Field: "level", Value: Literal("error")},
			map[string]any{"term": map[string]any{"level": "error"}}),
		Entry("term with qualifiers", Condition{Op: OpTerm, Field: "level", Value: Literal("error"), CaseInsensitive: boolPtr(true)},
			map[string]any{"term": map[string]any{"level": map[string]any{"value": "error", "case_insensitive": true}}}),
		Entry("terms", Condition{Op: OpTerms, Field: "level", Values: []Value{{Literal: "error"}, {Literal: "warn"}}},
			map[string]any{"terms": map[string]any{"level": []any{"error", "warn"}}}),
		Entry("match", Condition{Op: OpMatch, Field: "message", Value: Literal("timeout")},
			map[string]any{"match": map[string]any{"message": "timeout"}}),
		Entry("match with operator", Condition{Op: OpMatch, Field: "message", Value: Literal("timeout"), MatchOperator: "and"},
			map[string]any{"match": map[string]any{"message": map[string]any{"query": "timeout", "operator": "and"}}}),
		Entry("match_phrase", Condition{Op: OpMatchPhrase, Field: "message", Value: Literal("connection refused")},
			map[string]any{"match_phrase": map[string]any{"message": "connection refused"}}),
		Entry("match_phrase with slop", Condition{Op: OpMatchPhrase, Field: "message", Value: Literal("connection refused"), Slop: intPtr(2)},
			map[string]any{"match_phrase": map[string]any{"message": map[string]any{"query": "connection refused", "slop": 2}}}),
		Entry("match_phrase_prefix", Condition{Op: OpMatchPhrasePrefix, Field: "message", Value: Literal("conn")},
			map[string]any{"match_phrase_prefix": map[string]any{"message": "conn"}}),
		Entry("multi_match", Condition{Op: OpMultiMatch, Fields: []string{"message", "error"}, Value: Literal("boom"), MultiMatchType: "phrase"},
			map[string]any{"multi_match": map[string]any{"query": "boom", "fields": []string{"message", "error"}, "type": "phrase"}}),
		Entry("multi_match falls back to the single field", Condition{Op: OpMultiMatch, Field: "message", Value: Literal("boom")},
			map[string]any{"multi_match": map[string]any{"query": "boom", "fields": []string{"message"}}}),
		Entry("prefix", Condition{Op: OpPrefix, Field: "host", Value: Literal("web-")},
			map[string]any{"prefix": map[string]any{"host": "web-"}}),
		Entry("wildcard", Condition{Op: OpWildcard, Field: "host", Value: Literal("web-*")},
			map[string]any{"wildcard": map[string]any{"host": "web-*"}}),
		Entry("regexp always bounds the automaton", Condition{Op: OpRegexp, Field: "host", Value: Literal("web-[0-9]+")},
			map[string]any{"regexp": map[string]any{"host": map[string]any{"value": "web-[0-9]+", "max_determinized_states": maxDeterminizedStates}}}),
		Entry("fuzzy", Condition{Op: OpFuzzy, Field: "user", Value: Literal("jhon")},
			map[string]any{"fuzzy": map[string]any{"user": "jhon"}}),
		Entry("fuzzy with fuzziness", Condition{Op: OpFuzzy, Field: "user", Value: Literal("jhon"), Fuzziness: "AUTO"},
			map[string]any{"fuzzy": map[string]any{"user": map[string]any{"value": "jhon", "fuzziness": "AUTO"}}}),
		Entry("range with date math", Condition{Op: OpRange, Field: "@timestamp", Gte: Literal("now-1h"), Lte: Literal("now/d")},
			map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": "now-1h", "lte": "now/d"}}}),
		Entry("range with format and time zone", Condition{Op: OpRange, Field: "@timestamp", Gt: Literal("2024-01-01"), Format: "yyyy-MM-dd", TimeZone: "+02:00"},
			map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gt": "2024-01-01", "format": "yyyy-MM-dd", "time_zone": "+02:00"}}}),
		Entry("exists", Condition{Op: OpExists, Field: "trace_id"},
			map[string]any{"exists": map[string]any{"field": "trace_id"}}),
		Entry("ids", Condition{Op: OpIDs, Values: []Value{{Literal: "a"}, {Literal: "b"}}},
			map[string]any{"ids": map[string]any{"values": []any{"a", "b"}}}),
		Entry("query_string", Condition{Op: OpQueryString, Fields: []string{"message"}, Value: Literal("level:error AND host:web*")},
			map[string]any{"query_string": map[string]any{"query": "level:error AND host:web*", "fields": []string{"message"}}}),
		Entry("simple_query_string", Condition{Op: OpSimpleQueryString, Fields: []string{"message"}, Value: Literal("boom"), MatchOperator: "and"},
			map[string]any{"simple_query_string": map[string]any{"query": "boom", "fields": []string{"message"}, "default_operator": "and"}}),
		Entry("match_all", Condition{Op: OpMatchAll},
			map[string]any{"match_all": map[string]any{}}),
		Entry("boost forces the expanded form", Condition{Op: OpTerm, Field: "level", Value: Literal("error"), Boost: floatPtr(2)},
			map[string]any{"term": map[string]any{"level": map[string]any{"value": "error", "boost": float64(2)}}}),
	)
})

var _ = Describe("Compile bool composition", func() {
	It("buckets children by occur", func() {
		query := compileQuery(Condition{Op: OpBool, Conditions: []Condition{
			{Op: OpTerm, Field: "level", Value: Literal("error")},
			{Op: OpMatch, Occur: OccurMust, Field: "message", Value: Literal("boom")},
			{Op: OpTerm, Occur: OccurShould, Field: "host", Value: Literal("web-1")},
			{Op: OpTerm, Occur: OccurMustNot, Field: "env", Value: Literal("dev")},
		}})
		Expect(query).To(Equal(map[string]any{"bool": map[string]any{
			"filter":               []any{map[string]any{"term": map[string]any{"level": "error"}}},
			"must":                 []any{map[string]any{"match": map[string]any{"message": "boom"}}},
			"should":               []any{map[string]any{"term": map[string]any{"host": "web-1"}}},
			"must_not":             []any{map[string]any{"term": map[string]any{"env": "dev"}}},
			"minimum_should_match": 1,
		}}))
	})

	It("omits minimum_should_match when no should clause exists", func() {
		query := compileQuery(Condition{Op: OpBool, Conditions: []Condition{
			{Op: OpTerm, Field: "level", Value: Literal("error")},
		}})
		Expect(query).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"level": "error"}}},
		}}))
	})

	It("honours an explicit minimum_should_match", func() {
		query := compileQuery(Condition{Op: OpBool, MinimumShouldMatch: "2", Conditions: []Condition{
			{Op: OpTerm, Occur: OccurShould, Field: "host", Value: Literal("web-1")},
			{Op: OpTerm, Occur: OccurShould, Field: "host", Value: Literal("web-2")},
		}})
		Expect(query["bool"].(map[string]any)["minimum_should_match"]).To(Equal("2"))
	})

	It("nests groups recursively", func() {
		query := compileQuery(Condition{Op: OpBool, Conditions: []Condition{
			{Op: OpNested, Path: "tags", ScoreMode: "avg", Conditions: []Condition{
				{Op: OpTerm, Field: "tags.key", Value: Literal("env")},
			}},
		}})
		Expect(query).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"nested": map[string]any{
				"path":       "tags",
				"score_mode": "avg",
				"query": map[string]any{"bool": map[string]any{
					"filter": []any{map[string]any{"term": map[string]any{"tags.key": "env"}}},
				}},
			}}},
		}}))
	})

	It("selects everything when there is no query", func() {
		compiled, err := Compile(CompileRequest{Search: Search{}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body).To(Equal(map[string]any{"query": map[string]any{"match_all": map[string]any{}}}))
	})
})

var _ = Describe("Compile output shaping", func() {
	It("keeps size out of the body and reports it separately", func() {
		compiled, err := Compile(CompileRequest{Search: Search{Size: intPtr(50), From: intPtr(10)}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(50))
		Expect(compiled.From).To(Equal(10))
		Expect(compiled.Body).ToNot(HaveKey("size"))
		Expect(compiled.Body["from"]).To(Equal(10))
	})

	It("narrows a larger specification size to the page asked for", func() {
		compiled, err := Compile(CompileRequest{Search: Search{Size: intPtr(5000)}, PageSize: 200})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(200))
		Expect(compiled.Capped).To(BeFalse())
	})

	It("applies the page size when the specification sets none", func() {
		compiled, err := Compile(CompileRequest{PageSize: 200})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(200))
		Expect(compiled.Capped).To(BeFalse())
	})

	// A profile whose own size is smaller than the page asked for serves a short
	// page. Left unreported, that short page reads as the end of the index — so
	// a `size: 50` profile answered "50" as the total of a million documents.
	It("reports a specification size that holds the page below what was asked", func() {
		compiled, err := Compile(CompileRequest{Search: Search{Size: intPtr(50)}, PageSize: 200})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(50))
		Expect(compiled.Capped).To(BeTrue())
	})

	It("leaves the specification size alone when no page is asked for", func() {
		compiled, err := Compile(CompileRequest{Search: Search{Size: intPtr(50)}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(50))
		Expect(compiled.Capped).To(BeFalse())
	})

	It("renders sort, _source, fields and hit counting", func() {
		compiled, err := Compile(CompileRequest{Search: Search{
			Sort: []SortBy{
				{Field: "@timestamp", Order: "desc"},
				{Field: "level", Mode: "min", Missing: "_last", UnmappedType: "keyword"},
				{Field: "_score"},
			},
			Source:         &Source{Includes: []string{"message"}, Excludes: []string{"raw.*"}},
			StoredFields:   []string{"*"},
			Fields:         []string{"custom_field"},
			TrackTotalHits: &TrackTotalHits{Threshold: intPtr(5000)},
		}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["sort"]).To(Equal([]any{
			map[string]any{"@timestamp": map[string]any{"order": "desc"}},
			map[string]any{"level": map[string]any{"mode": "min", "missing": "_last", "unmapped_type": "keyword"}},
			"_score",
		}))
		Expect(compiled.Body["_source"]).To(Equal(map[string]any{"includes": []string{"message"}, "excludes": []string{"raw.*"}}))
		Expect(compiled.Body["stored_fields"]).To(Equal([]string{"*"}))
		Expect(compiled.Body["fields"]).To(Equal([]string{"custom_field"}))
		Expect(compiled.Body["track_total_hits"]).To(Equal(5000))
	})

	It("disables _source", func() {
		compiled, err := Compile(CompileRequest{Search: Search{Source: &Source{Enabled: boolPtr(false)}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["_source"]).To(Equal(false))
	})

	It("passes aggregations through untouched", func() {
		compiled, err := Compile(CompileRequest{Search: Search{
			Aggregations: map[string]json.RawMessage{"by_level": json.RawMessage(`{"terms":{"field":"level"}}`)},
		}})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["aggs"]).To(Equal(map[string]any{
			"by_level": map[string]any{"terms": map[string]any{"field": "level"}},
		}))
	})

})
