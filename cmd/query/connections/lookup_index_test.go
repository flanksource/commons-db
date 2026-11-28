package connections

import (
	"github.com/flanksource/clicky/entity"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch index lookup", func() {
	targets := opensearchinspect.RollupTargets([]opensearchinspect.Target{
		{Name: "logs-2026-11-01", Kind: "index"},
		{Name: "logs-2026-11-02", Kind: "index"},
		{Name: "orders", Kind: "index"},
		{Name: "orders-current", Kind: "alias"},
		{Name: ".kibana_1", Kind: "index", Hidden: true, System: true},
	})
	option := func(name, group, description string) entity.LookupNode {
		return entity.LookupNode{Kind: "text", Plain: name, Text: name, Group: group, Description: description}
	}

	It("offers wildcard groups and standalone targets, not their rotations or hidden indexes", func() {
		options, total := indexOptions(targets, "", 100)
		Expect(total).To(Equal(3))
		Expect(options).To(Equal(map[string]entity.LookupNode{
			"logs-*":         option("logs-*", "Patterns", "2 indexes"),
			"orders":         option("orders", "Indexes", ""),
			"orders-current": option("orders-current", "Aliases", ""),
		}))
	})

	It("finds a rotation by substring once the author searches", func() {
		options, _ := indexOptions(targets, "11-02", 100)
		Expect(options).To(Equal(map[string]entity.LookupNode{
			"logs-2026-11-02": option("logs-2026-11-02", "Indexes", ""),
		}))
	})

	It("offers a typed wildcard itself with the indexes it covers, beside the targets it matches", func() {
		options, total := indexOptions(targets, "logs-2026-11-*", 100)
		Expect(total).To(Equal(3))
		Expect(options).To(Equal(map[string]entity.LookupNode{
			"logs-2026-11-*":  option("logs-2026-11-*", "Pattern", "matches 2 indexes"),
			"logs-2026-11-01": option("logs-2026-11-01", "Indexes", ""),
			"logs-2026-11-02": option("logs-2026-11-02", "Indexes", ""),
		}))
	})

	It("still offers a wildcard that matches nothing, saying so", func() {
		options, total := indexOptions(targets, "metrics-*", 100)
		Expect(total).To(Equal(1))
		Expect(options).To(Equal(map[string]entity.LookupNode{
			"metrics-*": option("metrics-*", "Pattern", "matches no indexes"),
		}))
	})

	It("caps the options while reporting every match", func() {
		options, total := indexOptions(targets, "", 1)
		Expect(total).To(Equal(3))
		Expect(options).To(HaveLen(1))
	})
})
