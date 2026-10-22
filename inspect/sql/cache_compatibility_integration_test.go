package sqlinspect

import (
	"database/sql"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL inspection compatibility", func() {
	It("preserves the limits argument and isolates cached catalogues by those limits", func(ctx SpecContext) {
		client, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "catalogue.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(client.Close)
		for _, statement := range []string{"CREATE TABLE customers (id INTEGER)", "CREATE TABLE orders (id INTEGER)"} {
			_, err := client.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred())
		}
		options := Options{CacheKey: "compatibility-limits", Cache: NewMemoryCache()}
		bounded, err := Inspect(ctx, client, "sqlite", Limits{MaxRelations: 1}, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(bounded.Schemas[0].Relations).To(HaveLen(1))
		Expect(bounded.Truncated).To(BeTrue())
		complete, err := Inspect(ctx, client, "sqlite", Limits{MaxRelations: 2}, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(complete.Schemas[0].Relations).To(HaveLen(2))
		Expect(complete.Cache.Cached).To(BeFalse())
		cached, err := Inspect(ctx, client, "sqlite", Limits{MaxRelations: 2}, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.Cache.Cached).To(BeTrue())
	})
})
