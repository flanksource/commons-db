package connections

import (
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch group catalog", func() {
	It("selects the wildcard while keeping concrete rotations as children", func() {
		nodes := catalogNodesForOpenSearch(opensearchinspect.RollupTargets([]opensearchinspect.Target{
			{Name: "logs-2026-11-01", Kind: "index"},
			{Name: "logs-2026-11-02", Kind: "index"},
		}))
		Expect(nodes).To(HaveLen(1))
		Expect(nodes[0].ID).To(Equal("group:logs-*"))
		Expect(nodes[0].Options).To(Equal(map[string]any{"index": "logs-*", "targetKind": "group", "limit": "200"}))
		Expect(nodes[0].Children).To(HaveLen(2))
		Expect(nodes[0].Children[0].Options["index"]).To(Equal("logs-2026-11-01"))
	})
})
