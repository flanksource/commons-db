package metrics

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("label predicate pushdown", func() {
	DescribeTable("translates WHERE predicates into Prometheus-style matchers",
		func(where string, args []any, expected []Matcher) {
			var pushed []Matcher
			_, err := Execute(context.Background(), Request{SQL: `SELECT pod FROM cpu WHERE ` + where, Args: args, Labels: map[string]string{"pod": "pod"}}, func(s Source, _ func(Sample) error) error {
				pushed = s.Filters
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(pushed).To(Equal(expected))
		},
		Entry("equality", `pod = 'api'`, nil, []Matcher{{Label: "pod", Op: "=", Value: "api"}}),
		Entry("inequality", `pod != 'api'`, nil, []Matcher{{Label: "pod", Op: "!=", Value: "api"}}),
		Entry("IN as an anchored, escaped alternation", `pod IN ('api', 'web.1')`, nil, []Matcher{{Label: "pod", Op: "=~", Value: `^(api|web\.1)$`}}),
		Entry("NOT IN as a negative alternation", `pod NOT IN ('api')`, nil, []Matcher{{Label: "pod", Op: "!~", Value: `^(api)$`}}),
		Entry("LIKE as a case-insensitive regex", `pod LIKE 'api.%_x'`, nil, []Matcher{{Label: "pod", Op: "=~", Value: `(?i)^api\..*.x$`}}),
		Entry("NOT LIKE as a negative regex", `pod NOT LIKE 'api%'`, nil, []Matcher{{Label: "pod", Op: "!~", Value: `(?i)^api.*$`}}),
		Entry("bound IN values", `pod IN (__cdb_query_param_0__, 'web')`, []any{"api"}, []Matcher{{Label: "pod", Op: "=~", Value: `^(api|web)$`}}),
		Entry("bound LIKE pattern", `pod LIKE __cdb_query_param_0__`, []any{"api%"}, []Matcher{{Label: "pod", Op: "=~", Value: `(?i)^api.*$`}}),
		Entry("bound inequality", `pod != __cdb_query_param_0__`, []any{"api"}, []Matcher{{Label: "pod", Op: "!=", Value: "api"}}),
		Entry("disjunctions stay in SQLite", `pod = 'api' OR pod = 'web'`, nil, []Matcher{}),
	)

	It("still evaluates the WHERE clause in SQLite when the source ignores matchers", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT pod FROM cpu WHERE pod LIKE 'API%' AND pod NOT IN ('api-2') ORDER BY pod`, Labels: map[string]string{"pod": "pod"}}, func(_ Source, emit func(Sample) error) error {
			for _, pod := range []string{"api-1", "api-2", "web-1"} {
				if err := emit(Sample{Time: time.Unix(0, 0), Value: 1, Labels: map[string]string{"pod": pod}}); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"pod": "api-1"}}))
	})
})
