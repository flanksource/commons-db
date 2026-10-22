package database_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	oldatlas "github.com/flanksource/commons-db/atlas/sqlserver"
	"github.com/flanksource/commons-db/connection"
	oldinspect "github.com/flanksource/commons-db/inspect/sql"
	oldsnapshot "github.com/flanksource/commons-db/inspect/sql/snapshot"
	oldmigrate "github.com/flanksource/commons-db/migrate"
	oldsqlite "github.com/flanksource/commons-db/migrate/sqlite"
	nativeatlas "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	nativecapabilities "github.com/flanksource/sqlglot-go/database/capabilities"
	nativesnapshot "github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	nativemigrate "github.com/flanksource/sqlglot-go/database/migrate"
	nativesqlite "github.com/flanksource/sqlglot-go/database/migrate/sqlite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

var (
	_ nativemigrate.Option                   = oldmigrate.WithRebuilds()
	_ nativesqlite.ReconcileOptions          = oldsqlite.ReconcileOptions{AllowRebuilds: true}
	_ *nativeatlas.Driver                    = new(oldatlas.Driver)
	_ nativesnapshot.Result                  = oldsnapshot.Result{}
	_ nativecapabilities.BackendCapabilities = connection.BackendCapabilities{}
)

func TestCompatibility(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database API compatibility")
}

var _ = Describe("database compatibility shims", func() {
	It("applies portable HCL and retains commons catalog cache metadata", func(ctx SpecContext) {
		db, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "compat.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		files := fstest.MapFS{"schema.hcl": {Data: []byte(`schema "public" {}
table "items" {
 schema = schema.public
 column "id" { type = text }
}`)}}
		Expect(oldmigrate.Apply(ctx, "", files, oldmigrate.WithDB(db), oldmigrate.WithRebuilds())).To(Succeed())
		cache := oldinspect.NewMemoryCache()
		opts := oldinspect.Options{CacheKey: "compat-catalog", Cache: cache}
		first, err := oldinspect.Inspect(ctx, db, "sqlite", oldinspect.Limits{}, opts)
		Expect(err).NotTo(HaveOccurred())
		second, err := oldinspect.Inspect(ctx, db, "sqlite", oldinspect.Limits{}, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Schemas).To(Equal(second.Schemas))
		Expect(second.Cache).NotTo(BeNil())
		Expect(second.Schemas[0].Relations[0].Name).To(Equal("items"))
		Expect(second.Capabilities.Require(connection.BackendCapabilityArrayFilters)).To(Succeed())
		Expect(nativemigrate.Apply(context.Background(), "", files, oldmigrate.WithDB(db))).To(Succeed())
	})
})
