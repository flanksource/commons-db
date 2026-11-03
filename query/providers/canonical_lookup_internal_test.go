package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical status options", func() {
	It("offers unset for missing source values without claiming native bucket counts", func() {
		resolved := layout.Definition{Fields: map[string]layout.Field{"status": {Source: "tag.error", Type: "boolean", Values: map[string]any{"unset": false, "error": true}}}}
		options, total, err := lookupCanonicalFilterValues(context.New(), nil, openSearchValuesSource{}, query.FilterValueSearch{Binding: query.ColumnFilterBinding{Field: "status"}, Search: "un"}, &resolved)
		Expect(err).NotTo(HaveOccurred())
		Expect(options).To(Equal([]query.FilterOption{{Value: "unset"}}))
		Expect(total).To(Equal(&query.Total{Value: 1, Exact: true}))
	})
})
