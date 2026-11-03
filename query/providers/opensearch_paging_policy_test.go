package providers

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch paging policy", func() {
	DescribeTable("selects by requested rows rather than batch size", func(page query.PageRequest, cursor bool) {
		Expect((openSearchPagingPolicy{threshold: 10000}).usesCursor(page)).To(Equal(cursor))
	}, Entry("below", query.PageRequest{Limit: 9999}, false), Entry("equal", query.PageRequest{Limit: 10000}, false), Entry("above", query.PageRequest{Limit: 10001}, true), Entry("small export", query.PageRequest{Limit: 100, ReadAll: true, Ceiling: 10000}, false), Entry("large export", query.PageRequest{Limit: 100, ReadAll: true, Ceiling: 10001}, true), Entry("unbounded", query.PageRequest{Limit: 100, ReadAll: true}, true))
	DescribeTable("resolves independent profile overrides", func(properties map[string]string, options OpenSearchPagingOptions, mode openSearchPagingMode, threshold int) {
		policy, err := resolveOpenSearchPagingPolicy(&models.Connection{Properties: properties}, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(policy).To(Equal(openSearchPagingPolicy{mode: mode, threshold: threshold}))
	},
		Entry("defaults", nil, OpenSearchPagingOptions{}, openSearchPagingScroll, 10000),
		Entry("connection", map[string]string{"paging_mode": "pit", "paging_threshold": "1000"}, OpenSearchPagingOptions{}, openSearchPagingPIT, 1000),
		Entry("mode only", map[string]string{"paging_threshold": "1000"}, OpenSearchPagingOptions{PagingMode: "pit"}, openSearchPagingPIT, 1000),
		Entry("explicit zero", map[string]string{"paging_mode": "pit"}, OpenSearchPagingOptions{PagingThreshold: new(0)}, openSearchPagingPIT, 0),
	)
	DescribeTable("rejects invalid thresholds", func(value string) {
		_, err := resolveOpenSearchPagingPolicy(&models.Connection{Properties: map[string]string{"paging_threshold": value}}, OpenSearchPagingOptions{})
		Expect(err).To(HaveOccurred())
	}, Entry("negative", "-1"), Entry("above window", "10001"), Entry("malformed", "many"))
})
