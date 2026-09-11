package connections

import (
	"encoding/json"

	dbconnection "github.com/flanksource/commons-db/connection"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection capability projections", func() {
	capabilities := dbconnection.BackendCapabilities{
		Backend: "sqlserver", Version: "12.0.2000.8", Database: "warehouse",
		CompatibilityLevel: func() *int { value := 160; return &value }(),
		Features: map[dbconnection.BackendCapability]dbconnection.BackendCapabilityStatus{
			dbconnection.BackendCapabilityArrayFilters: {Supported: true},
		},
	}

	It("carries the shared capability result in connection information", func() {
		info := sqlBackendInfo(models.ConnectionTypeSQLServer, capabilities)

		Expect(info.Product).To(Equal("SQL Server"))
		Expect(info.Version).To(Equal(capabilities.Version))
		Expect(info.Database).To(Equal(capabilities.Database))
		Expect(info.Capabilities).To(HaveValue(Equal(capabilities)))
	})

	It("carries the catalog capability result in browser inspection", func() {
		inspection := sqlBrowserInspection(models.ConnectionTypeSQLServer, sqlinspect.Catalog{
			Driver: "sqlserver", Database: "warehouse", Capabilities: capabilities,
		})

		Expect(inspection.Capabilities).To(HaveValue(Equal(capabilities)))
		Expect(inspection.Dialect).To(Equal("mssql"))
	})

	It("omits SQL capability metadata from non-SQL inspections", func() {
		encoded, err := json.Marshal(browserInspection{Kind: "opensearch"})
		Expect(err).ToNot(HaveOccurred())
		Expect(encoded).To(MatchJSON(`{"kind":"opensearch"}`))
	})
})
