package esdsl

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Bind operands", func() {
	It("substitutes a parameter structurally, preserving its type", func() {
		query := compileQuery(
			Condition{Op: OpTerm, Field: "status", Value: Param("status")},
			ParamBinding{Name: "status", Role: RoleFilter, Value: 500},
		)
		Expect(query).To(Equal(map[string]any{"term": map[string]any{"status": 500}}))
	})

	It("never interpolates a parameter into a query string", func() {
		query := compileQuery(
			Condition{Op: OpQueryString, Fields: []string{"message"}, Value: Param("q")},
			ParamBinding{Name: "q", Value: `boom" OR level:*`},
		)
		Expect(query["query_string"].(map[string]any)["query"]).To(Equal(`boom\" OR level\:\*`))
	})

	It("expands a comma-separated parameter into a terms list", func() {
		query := compileQuery(
			Condition{Op: OpTerms, Field: "level", Value: Param("levels")},
			ParamBinding{Name: "levels", Value: "error, warn ,fatal"},
		)
		Expect(query).To(Equal(map[string]any{"terms": map[string]any{"level": []any{"error", "warn", "fatal"}}}))
	})

	It("expands a list-valued parameter", func() {
		query := compileQuery(
			Condition{Op: OpTerms, Field: "level", Value: Param("levels")},
			ParamBinding{Name: "levels", Value: []string{"error", "warn"}},
		)
		Expect(query).To(Equal(map[string]any{"terms": map[string]any{"level": []any{"error", "warn"}}}))
	})

	It("reports every condition field that structurally binds a parameter", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{Query: &Condition{Op: OpBool, Conditions: []Condition{
				{Op: OpTerm, Field: "service.name", Value: Param("service")},
				{Op: OpTerm, Field: "peer.service", Value: Param("service")},
				{Op: OpTerms, Field: "scheme.id", Value: Param("schemes")},
			}}},
			Params: []ParamBinding{
				{Name: "service", Value: "api"},
				{Name: "schemes", Value: []string{"one", "two"}},
			},
			Referenced: []string{"outside"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.ParamUses).To(Equal([]ParamUse{
			{Name: "service", Field: "service.name"},
			{Name: "service", Field: "peer.service"},
			{Name: "schemes", Field: "scheme.id"},
		}))
	})

	It("passes date math through verbatim", func() {
		query := compileQuery(
			Condition{Op: OpRange, Field: "@timestamp", Gte: Param("since"), Lt: Literal("2024-01-01||+1M")},
			ParamBinding{Name: "since", Value: "now-1h/d"},
		)
		Expect(query).To(Equal(map[string]any{"range": map[string]any{"@timestamp": map[string]any{
			"gte": "now-1h/d",
			"lt":  "2024-01-01||+1M",
		}}}))
	})
})

var _ = Describe("Bind pruning", func() {
	It("drops an optional condition whose parameter was not supplied", func() {
		query := compileQuery(Condition{Op: OpBool, Conditions: []Condition{
			{Op: OpTerm, Field: "level", Value: Param("level"), Optional: true},
			{Op: OpTerm, Field: "host", Value: Literal("web-1")},
		}})
		Expect(query).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"host": "web-1"}}},
		}}))
	})

	It("drops an optional condition whose parameter resolved to empty", func() {
		query := compileQuery(
			Condition{Op: OpTerm, Field: "level", Value: Param("level"), Optional: true},
			ParamBinding{Name: "level", Value: "   "},
		)
		Expect(query).To(Equal(map[string]any{"match_all": map[string]any{}}))
	})

	It("selects everything when every condition prunes", func() {
		query := compileQuery(Condition{Op: OpBool, Conditions: []Condition{
			{Op: OpTerms, Field: "level", Value: Param("levels"), Optional: true},
			{Op: OpRange, Field: "@timestamp", Gte: Param("since"), Optional: true},
		}})
		Expect(query).To(Equal(map[string]any{"match_all": map[string]any{}}))
	})

	DescribeTable("gates a valueless condition on the parameter named by when",
		func(params []ParamBinding, expected map[string]any) {
			compiled, err := Compile(CompileRequest{
				Search: Search{Query: &Condition{Op: OpExists, Field: "error", When: "onlyErrors"}},
				Params: params,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(compiled.Body["query"]).To(Equal(expected))
		},
		Entry("supplied", []ParamBinding{{Name: "onlyErrors", Value: "true"}},
			map[string]any{"exists": map[string]any{"field": "error"}}),
		Entry("absent", nil,
			map[string]any{"match_all": map[string]any{}}),
		Entry("supplied but empty", []ParamBinding{{Name: "onlyErrors", Value: ""}},
			map[string]any{"match_all": map[string]any{}}),
	)

	It("gates a whole group on when, and counts the gate as a use", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{Query: &Condition{Op: OpBool, When: "degraded", Conditions: []Condition{
				{Op: OpTerm, Field: "level", Value: Literal("error")},
			}}},
			Params: []ParamBinding{{Name: "degraded", Role: RoleFilter, Value: "yes"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"level": "error"}}},
		}}))
	})

	It("fails when a required parameter has no value", func() {
		_, err := Compile(CompileRequest{Search: Search{
			Query: &Condition{Op: OpTerm, Field: "level", Value: Param("level")},
		}})
		Expect(err).To(MatchError(ContainSubstring(`query: param "level" has no value`)))
		Expect(err).To(MatchError(ContainSubstring("declare it optional")))
	})

	It("fails when a supplied filter parameter is referenced nowhere", func() {
		_, err := Compile(CompileRequest{
			Search: Search{Query: &Condition{Op: OpTerm, Field: "host", Value: Literal("web-1")}},
			Params: []ParamBinding{{Name: "level", Role: RoleFilter, Value: "error"}},
		})
		Expect(err).To(MatchError(ContainSubstring(`param "level" is not referenced by the search specification`)))
		Expect(err).To(MatchError(ContainSubstring(`bind it as {"param":"level"}`)))
		Expect(err).To(MatchError(ContainSubstring(`interpolate it as {{.params.level}}`)))
	})

	It("accepts a parameter the caller already consumed by interpolation", func() {
		compiled, err := Compile(CompileRequest{
			// The operand arrives already interpolated, so nothing here binds
			// "country" structurally — only Referenced proves it was used.
			Search:     Search{Query: &Condition{Op: OpTerm, Field: "service", Value: Literal("kenya-api")}},
			Params:     []ParamBinding{{Name: "country", Role: RoleFilter, Value: "kenya"}},
			Referenced: []string{"country"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["query"]).To(Equal(map[string]any{"term": map[string]any{"service": "kenya-api"}}))
	})

	It("still reports a parameter that neither binding nor interpolation reached", func() {
		_, err := Compile(CompileRequest{
			Search: Search{Query: &Condition{Op: OpTerm, Field: "service", Value: Literal("kenya-api")}},
			Params: []ParamBinding{
				{Name: "country", Role: RoleFilter, Value: "kenya"},
				{Name: "level", Role: RoleFilter, Value: "error"},
			},
			Referenced: []string{"country"},
		})
		Expect(err).To(MatchError(ContainSubstring(`param "level" is not referenced`)))
	})

	It("rejects a duplicate parameter binding", func() {
		_, err := Compile(CompileRequest{Params: []ParamBinding{
			{Name: "level", Value: "error"},
			{Name: "level", Value: "warn"},
		}})
		Expect(err).To(MatchError(ContainSubstring(`parameter "level" is bound twice`)))
	})
})

var _ = Describe("Bind parameter roles", func() {
	It("folds time-from and time-to into a single range on the time field", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{
				TimeField: "@timestamp",
				Query:     &Condition{Op: OpTerm, Field: "level", Value: Literal("error")},
			},
			Params: []ParamBinding{
				{Name: "from", Role: RoleTimeFrom, Value: "now-24h"},
				{Name: "to", Role: RoleTimeTo, Value: "now"},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"term": map[string]any{"level": "error"}},
				map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": "now-24h", "lte": "now"}}},
			},
		}}))
	})

	It("appends the time range to an existing bool rather than nesting one", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{
				TimeField: "@timestamp",
				Query: &Condition{Op: OpBool, Conditions: []Condition{
					{Op: OpTerm, Occur: OccurMustNot, Field: "env", Value: Literal("dev")},
				}},
			},
			Params: []ParamBinding{{Name: "from", Role: RoleTimeFrom, Value: "now-1h"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"must_not": []any{map[string]any{"term": map[string]any{"env": "dev"}}},
			"filter":   []any{map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": "now-1h"}}}},
		}}))
	})

	It("replaces a match_all root with the time range instead of filtering on both", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{TimeField: "@timestamp", Query: &Condition{Op: OpMatchAll}},
			Params: []ParamBinding{{Name: "from", Role: RoleTimeFrom, Value: "now-1h"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": "now-1h"}}}},
		}}))
	})

	It("fails when a time parameter has no time field to bind to", func() {
		_, err := Compile(CompileRequest{
			Params: []ParamBinding{{Name: "from", Role: RoleTimeFrom, Value: "now-1h"}},
		})
		Expect(err).To(MatchError(ContainSubstring("requires timeField on the search specification")))
	})

	It("maps limit and offset roles onto size and from", func() {
		compiled, err := Compile(CompileRequest{
			Search: Search{Size: intPtr(10)},
			Params: []ParamBinding{
				{Name: "limit", Role: RoleLimit, Value: 25},
				{Name: "offset", Role: RoleOffset, Value: "40"},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(compiled.Size).To(Equal(25))
		Expect(compiled.From).To(Equal(40))
		Expect(compiled.Body["from"]).To(Equal(40))
	})

	It("rejects a limit parameter that is not a whole number", func() {
		_, err := Compile(CompileRequest{Params: []ParamBinding{
			{Name: "limit", Role: RoleLimit, Value: "many"},
		}})
		Expect(err).To(MatchError(ContainSubstring(`limit parameter: value "many" is not a whole number`)))
	})

	It("rejects a negative offset parameter", func() {
		_, err := Compile(CompileRequest{Params: []ParamBinding{
			{Name: "offset", Role: RoleOffset, Value: -1},
		}})
		Expect(err).To(MatchError(ContainSubstring("offset parameter must not be negative")))
	})
})
