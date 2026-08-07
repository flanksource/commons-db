package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = DescribeTable("FilterFieldForJSONPath",
	func(source, path, expected string) {
		field, ok := query.FilterFieldForJSONPath(path, source)
		Expect(ok).To(Equal(expected != ""))
		Expect(field).To(Equal(expected))
	},
	Entry("a row-rooted literal chain names the nested field",
		"", "$.metadata.user.email", "metadata.user.email"),
	Entry("a source-rooted literal chain is prefixed by the source",
		"metadata", "$.user.email", "metadata.user.email"),
	Entry("bracket notation reads the same as dotted notation",
		"", `$["metadata"]["user"]["email"]`, "metadata.user.email"),
	Entry("a single-segment path under a source names one nested field",
		"payload", "$.status", "payload.status"),
	Entry("a bare root under a source is the source itself",
		"payload", "$", "payload"),
	Entry("a filter expression selects rows rather than naming a field",
		"tags", "$[?(@.key == 'http.status')].value", ""),
	Entry("a wildcard names no single field", "", "$.metadata.*", ""),
	Entry("a descent names no single field", "", "$..email", ""),
	Entry("an array index names no backend field", "tags", "$[0].value", ""),
	Entry("a bare root with no source names nothing", "", "$", ""),
	Entry("an unparseable path names nothing", "", "$.[", ""),
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
