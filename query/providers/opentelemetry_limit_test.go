package providers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

var _ = Describe("opentelemetry limit option", func() {
	// The profile builder's Limit field writes a string, as it does for every
	// other provider, and stored opensearch/jaeger/loki profiles carry one too.
	It("decodes a string limit and sizes the trace search with it", func() {
		const limit = 200
		options, err := providerkit.DecodeOptions[OpenTelemetryOptions](map[string]any{"limit": "200"})
		Expect(err).NotTo(HaveOccurred())

		search, err := openTelemetrySearch(options)
		Expect(err).NotTo(HaveOccurred())
		Expect(search.Size).To(HaveValue(Equal(limit)))
	})

	It("leaves an explicit search size alone", func() {
		const size = 10
		options, err := providerkit.DecodeOptions[OpenTelemetryOptions](map[string]any{
			"limit":  "200",
			"search": map[string]any{"size": size},
		})
		Expect(err).NotTo(HaveOccurred())

		search, err := openTelemetrySearch(options)
		Expect(err).NotTo(HaveOccurred())
		Expect(search.Size).To(HaveValue(Equal(size)))
	})

	It("refuses a limit that is no count at decode time", func() {
		_, err := providerkit.DecodeOptions[OpenTelemetryOptions](map[string]any{"limit": "many"})
		Expect(err).To(MatchError(ContainSubstring(`invalid opensearch limit "many"`)))
	})
})
