package sqlinspect

import (
	"github.com/flanksource/commons-db/connection"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL catalog capabilities", func() {
	It("carries the live backend metadata on the catalog", func() {
		capabilities := connection.BackendCapabilities{
			Backend: "postgres", Version: "17.4", Database: "warehouse",
			Features: map[connection.BackendCapability]connection.BackendCapabilityStatus{
				connection.BackendCapabilityArrayFilters: {Supported: true},
			},
		}
		catalog := buildCatalog("postgres", "warehouse", "public", []string{"warehouse"}, []string{"public"}, nil, Limits{}, capabilities)

		Expect(catalog.Capabilities).To(Equal(capabilities))
	})
})
