package connection

import (
	"context"
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("backend capability integration", func() {
	It("probes the linked SQLite driver's real JSON implementation", func() {
		db, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })

		capabilities, err := ProbeBackendCapabilities(context.Background(), db, "sqlite")
		Expect(err).ToNot(HaveOccurred())
		Expect(capabilities.Require(BackendCapabilityArrayFilters)).To(Succeed())
	})
})
