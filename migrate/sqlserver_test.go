package migrate

import (
	"testing/fstest"

	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	sqlServerDSN  = "sqlserver://sa:secret@localhost:1433?database=acme"
	purgeRoutine  = "CREATE OR ALTER PROCEDURE dbo.acme_purge_orders @before DATE AS BEGIN DELETE FROM dbo.acme_orders WHERE created_at < @before; END"
	ordersHCLFile = `schema "dbo" {}
table "acme_orders" {
  schema = schema.dbo
  column "id" {
    type = int
  }
  column "created_at" {
    type = date
  }
  primary_key {
    columns = [column.id]
  }
}
`
)

func file(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

var _ = Describe("SQL Server routine files", func() {
	DescribeTable("loads one routine per file, named after the file",
		func(name, content string) {
			routines, err := loadRoutines(fstest.MapFS{"routines/" + name: file(content)}, "routines")
			Expect(err).ToNot(HaveOccurred())
			Expect(routines).To(HaveLen(1))
			Expect(routines[0].Name).To(Equal("acme_purge_orders"))
			Expect(routines[0].Text).To(Equal(purgeRoutine))
		},
		Entry("a bare definition", "acme_purge_orders.sql", purgeRoutine),
		Entry("a trailing GO", "acme_purge_orders.sql", purgeRoutine+"\nGO\n"),
	)

	DescribeTable("refuses a file that is not exactly one routine named after it",
		func(name, content, message string) {
			_, err := loadRoutines(fstest.MapFS{"routines/" + name: file(content)}, "routines")
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a name mismatch", "acme_purge.sql", purgeRoutine,
			`routines/acme_purge.sql creates "acme_purge_orders": the file must be named after its routine`),
		Entry("two routines in two batches", "acme_purge_orders.sql", purgeRoutine+"\nGO\n"+purgeRoutine,
			"expected exactly one batch, found 2"),
		Entry("two routines in one batch", "acme_purge_orders.sql", purgeRoutine+"\n"+purgeRoutine,
			"sqlserver: parse routine"),
		Entry("a table", "acme_purge_orders.sql", "CREATE TABLE dbo.acme_purge_orders (id INT)",
			"makes no routine"),
	)

	It("refuses an empty routine directory", func() {
		_, err := loadRoutines(fstest.MapFS{"routines/README.md": file("acme")}, "routines")
		Expect(err).To(MatchError(ContainSubstring("no .sql files found")))
	})
})

var _ = Describe("SQL Server migration bundle", func() {
	bundle := func() fstest.MapFS {
		return fstest.MapFS{
			"db/schema.hcl":                     file(ordersHCLFile),
			"db/routines/acme_purge_orders.sql": file(purgeRoutine),
			"db/routines/acme_count_orders.sql": file("CREATE PROCEDURE acme_count_orders AS BEGIN SELECT COUNT(*) FROM dbo.acme_orders; END\nGO"),
		}
	}

	It("builds the desired dbo schema from HCL tables and routine files, which are not scripts", func() {
		realm, err := sqlServerDesired(bundle(), resolveOptions([]Option{WithDir("db"), WithRoutines("db/routines")}))
		Expect(err).ToNot(HaveOccurred())
		dbo := realm.Schemas[0]
		Expect(dbo.Name).To(Equal("dbo"))
		Expect(dbo.Tables).To(HaveLen(1))
		Expect([]string{dbo.Procs[0].Name, dbo.Procs[1].Name}).To(ConsistOf("acme_purge_orders", "acme_count_orders"))
		Expect(sqlServerInspectOptions(dbo, nil)).To(Equal(&schema.InspectOptions{
			Mode:   schema.InspectSchemas | schema.InspectTables | schema.InspectFuncs,
			Tables: []string{"acme_orders", dbo.Procs[0].Name, dbo.Procs[1].Name},
		}))
	})

	It("builds routines alone when the bundle has no HCL", func() {
		realm, err := sqlServerDesired(fstest.MapFS{"routines/acme_purge_orders.sql": file(purgeRoutine)},
			resolveOptions([]Option{WithRoutines("routines")}))
		Expect(err).ToNot(HaveOccurred())
		Expect(realm.Schemas[0].Procs).To(HaveLen(1))
		Expect(sqlServerInspectOptions(realm.Schemas[0], nil).Mode).To(Equal(schema.InspectSchemas | schema.InspectFuncs))
	})

	DescribeTable("refuses what SQL Server migrations do not support before connecting",
		func(filesystem fstest.MapFS, opts []Option, message string) {
			err := Apply(GinkgoT().Context(), sqlServerDSN, filesystem, opts...)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an empty bundle", fstest.MapFS{}, nil, `declares no tables or routines`),
		Entry("a SQL script outside the routine directory",
			fstest.MapFS{"db/routines/acme_purge_orders.sql": file(purgeRoutine), "db/001_seed.sql": file("SELECT 1")},
			[]Option{WithDir("db"), WithRoutines("db/routines")}, "do not support SQL migration scripts, found 001_seed.sql"),
		Entry("another schema", fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
			[]Option{WithDir("db"), WithSchema("tenant_x")}, `support only schema "dbo", got "tenant_x"`),
		Entry("drops", fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
			[]Option{WithDir("db"), WithDrops()}, "do not support destructive changes"),
		Entry("rebuilds", fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
			[]Option{WithDir("db"), WithRebuilds()}, "do not support table rebuilds"),
		Entry("HCL in another schema", fstest.MapFS{"db/schema.hcl": file(`schema "tenant_x" {}`)},
			[]Option{WithDir("db")}, `must declare exactly schema "dbo", got ["tenant_x"]`),
	)

	DescribeTable("WithRoutines is refused by the other dialects",
		func(dsn, message string) {
			err := Apply(GinkgoT().Context(), dsn, fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
				WithDir("db"), WithRoutines("db/routines"))
			Expect(err).To(MatchError(message))
		},
		Entry("PostgreSQL", "postgres://acme@localhost:5432/acme",
			"PostgreSQL migrations do not support routines (WithRoutines); only SQL Server does"),
		Entry("SQLite, before the file is opened", "sqlite://acme-never-created.db",
			"SQLite migrations do not support routines (WithRoutines); only SQL Server does"),
	)

	It("keeps undeclared routines and indexes of declared tables", func() {
		table := schema.NewTable("acme_orders")
		kept := &schema.AddColumn{C: schema.NewIntColumn("priority", "int")}
		changes := withoutSQLServerDrops([]schema.Change{
			&schema.DropProc{P: &schema.Proc{Name: "acme_legacy"}},
			&schema.DropFunc{F: &schema.Func{Name: "acme_legacy_total"}},
			&schema.ModifyTable{T: table, Changes: []schema.Change{&schema.DropIndex{I: schema.NewIndex("IX_dba_tuning")}, kept}},
			&schema.ModifyTable{T: table, Changes: []schema.Change{&schema.DropIndex{I: schema.NewIndex("IX_dba_other")}}},
		})
		Expect(changes).To(Equal([]schema.Change{&schema.ModifyTable{T: table, Changes: []schema.Change{kept}}}))
	})
})
