package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = DescribeTable("FilterTargetForJSONPath",
	func(source, path string, expected query.JSONPathFilterTarget) {
		target, ok := query.FilterTargetForJSONPath(path, source)
		Expect(ok).To(Equal(expected.Field != ""))
		Expect(target).To(Equal(expected))
	},
	Entry("a row-rooted literal chain names the nested field",
		"", "$.metadata.user.email", query.JSONPathFilterTarget{Field: "metadata.user.email"}),
	Entry("a source-rooted literal chain is prefixed by the source",
		"metadata", "$.user.email", query.JSONPathFilterTarget{Field: "metadata.user.email"}),
	Entry("bracket notation reads the same as dotted notation",
		"", `$["metadata"]["user"]["email"]`, query.JSONPathFilterTarget{Field: "metadata.user.email"}),
	Entry("a single-segment path under a source names one nested field",
		"payload", "$.status", query.JSONPathFilterTarget{Field: "payload.status"}),
	Entry("a bare root under a source is the source itself",
		"payload", "$", query.JSONPathFilterTarget{Field: "payload"}),

	Entry("a key equality addresses one entry of a tag list",
		"", "$.tags[?(@.key == 'http.status')].value", query.JSONPathFilterTarget{
			Field: "tags.value", Container: "tags",
			Where: map[string]string{"tags.key": "http.status"},
		}),
	Entry("the tag list may be the source column",
		"tags", "$[?(@.key == 'env')].value", query.JSONPathFilterTarget{
			Field: "tags.value", Container: "tags",
			Where: map[string]string{"tags.key": "env"},
		}),
	Entry("the literal may be written on the left",
		"", "$.tags[?('env' == @.key)].value", query.JSONPathFilterTarget{
			Field: "tags.value", Container: "tags",
			Where: map[string]string{"tags.key": "env"},
		}),
	Entry("a conjunction pins every constant it names",
		"", "$.tags[?(@.key == 'env' && @.type == 'str')].value", query.JSONPathFilterTarget{
			Field: "tags.value", Container: "tags",
			Where: map[string]string{"tags.key": "env", "tags.type": "str"},
		}),
	Entry("a deep container is addressed by its full path",
		"", "$.process.tags[?(@.key == 'host')].value", query.JSONPathFilterTarget{
			Field: "process.tags.value", Container: "process.tags",
			Where: map[string]string{"process.tags.key": "host"},
		}),

	Entry("a filter selecting whole entries names no field",
		"", "$.tags[?(@.key == 'env')]", query.JSONPathFilterTarget{}),
	Entry("an inequality selects a range rather than one entry",
		"", "$.tags[?(@.weight > 3)].value", query.JSONPathFilterTarget{}),
	Entry("a disjunction selects more entries than the value came from",
		"", "$.tags[?(@.key == 'env' || @.key == 'team')].value", query.JSONPathFilterTarget{}),
	Entry("a filter with nothing ahead of it has no container",
		"", "$[?(@.key == 'env')].value", query.JSONPathFilterTarget{}),
	Entry("a second filter picks an entry of an entry",
		"", "$.a[?(@.k == 'x')].b[?(@.k == 'y')].c", query.JSONPathFilterTarget{}),
	Entry("a non-literal operand is not a constant",
		"", "$.tags[?(@.key == @.value)].value", query.JSONPathFilterTarget{}),
	Entry("a deep relative path is not a key of the entry",
		"", "$.tags[?(@.meta.key == 'env')].value", query.JSONPathFilterTarget{}),

	Entry("a wildcard names no single field", "", "$.metadata.*", query.JSONPathFilterTarget{}),
	Entry("a descent names no single field", "", "$..email", query.JSONPathFilterTarget{}),
	Entry("an array index names no backend field", "tags", "$[0].value", query.JSONPathFilterTarget{}),
	Entry("a bare root with no source names nothing", "", "$", query.JSONPathFilterTarget{}),
	Entry("an unparseable path names nothing", "", "$.[", query.JSONPathFilterTarget{}),
)

var _ = Describe("EvalJSONPath", func() {
	// The row an authoring tool has in front of it: a decoded nested column, a
	// column carrying JSON as text (what an HTTP provider hands back), and a
	// repeated structure a wildcard can select several values out of.
	row := query.Row{
		"metadata": map[string]any{"user": map[string]any{"email": "ada@example.com"}},
		"payload":  `{"status":"OPEN","items":[{"sku":"a"},{"sku":"b"}]}`,
		"tags": []any{
			map[string]any{"key": "env", "value": "prod"},
			map[string]any{"key": "team", "value": "core"},
		},
		"absent": nil,
	}

	It("resolves a row-rooted path to the single value it addresses", func() {
		Expect(query.EvalJSONPath("$.metadata.user.email", "", row)).
			To(Equal([]any{"ada@example.com"}))
	})

	It("decodes a source column carrying JSON as text before applying the path", func() {
		Expect(query.EvalJSONPath("$.status", "payload", row)).To(Equal([]any{"OPEN"}))
	})

	It("returns every match a wildcard selects, in document order", func() {
		Expect(query.EvalJSONPath("$.tags[*].value", "", row)).
			To(Equal([]any{"prod", "core"}))
	})

	It("returns no matches rather than an error for a path the row does not carry", func() {
		matches, err := query.EvalJSONPath("$.metadata.user.phone", "", row)
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeEmpty())
	})

	It("returns no matches when the source column is null", func() {
		matches, err := query.EvalJSONPath("$.status", "absent", row)
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeEmpty())
	})

	It("reports the path that failed to parse", func() {
		_, err := query.EvalJSONPath("$.[", "", row)
		Expect(err).To(MatchError(ContainSubstring(`jsonpath "$.[" is invalid`)))
	})

	It("reports a source column holding a string that is not JSON", func() {
		_, err := query.EvalJSONPath("$.status", "metadata", query.Row{"metadata": "not json"})
		Expect(err).To(MatchError(ContainSubstring("is not JSON")))
	})
})
