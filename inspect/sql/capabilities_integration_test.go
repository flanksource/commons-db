package sqlinspect

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/connection"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL catalog capability integration", func() {
	It("surfaces the live SQLite capability result", func() {
		db, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })

		catalog, err := Inspect(context.Background(), db, "sqlite", Limits{}, Options{CacheKey: "sqlite-capabilities-integration"})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalog.Capabilities.Require(connection.BackendCapabilityArrayFilters)).To(Succeed())
	})
})
