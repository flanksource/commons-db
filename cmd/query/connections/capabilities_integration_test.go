package connections

import (
	"context"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection capability integration", func() {
	It("surfaces capabilities from a live SQLite connection", func() {
		info, err := discoverSQLBackend(
			context.Background(),
			dbcontext.NewContext(context.Background()),
			&models.Connection{Type: models.ConnectionTypeSQLite, URL: ":memory:"},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Capabilities).ToNot(BeNil())
		Expect(info.Capabilities.Require(dbconnection.BackendCapabilityArrayFilters)).To(Succeed())
	})
})
