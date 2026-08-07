package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("jsonPathFilterField",
	func(source, path, expected string) {
		field, ok := jsonPathFilterField(ColumnDef{Name: "column", Source: source, JSONPath: path})
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
)
