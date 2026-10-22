package providerkit_test

import (
	"math"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

func TestProviderkit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Providerkit Suite")
}

var _ = Describe("FieldBatches", fieldBatchesSpecs)

func fieldBatchesSpecs() {
	fields := []string{"a", "b", "c", "d", "e"}

	DescribeTable("splits fields into consecutive batches of at most size",
		func(size int, expected [][]string) {
			Expect(providerkit.FieldBatches(fields, size)).To(Equal(expected))
		},
		Entry("an uneven split", 2, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}),
		Entry("an even split", 5, [][]string{{"a", "b", "c", "d", "e"}}),
		Entry("a size whose capacity rounding would overflow", math.MaxInt, [][]string{{"a", "b", "c", "d", "e"}}),
	)

	It("returns no batches for no fields", func() {
		Expect(providerkit.FieldBatches(nil, 2)).To(BeEmpty())
	})
}
