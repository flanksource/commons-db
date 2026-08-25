package connections

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection logging validation", func() {
	DescribeTable("rejects invalid persisted properties",
		func(property, value string) {
			err := validateConnection(nil, &models.Connection{
				Type:       models.ConnectionTypeHTTP,
				Properties: types.JSONStringMap{property: value},
			})
			Expect(err).To(MatchError(ContainSubstring(property)))
		},
		Entry("unknown level", observability.PropertyHTTPLevel, "verbose"),
		Entry("invalid threshold", observability.PropertySlowThreshold, "later"),
	)
})
