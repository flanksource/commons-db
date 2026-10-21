package sqlserver_test

import (
	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

const ordersHCL = `
schema "dbo" {}

table "acme_orders" {
  schema = schema.dbo
  column "id" {
    type = int
    identity {
      seed      = 1
      increment = 1
    }
  }
  column "status" {
    type    = nvarchar(20)
    default = "open"
  }
  column "note" {
    type = sql("nvarchar(max)")
    null = true
  }
  column "total" {
    type = decimal(18, 2)
  }
  primary_key {
    columns = [column.id]
  }
  index "IX_acme_orders_status" {
    columns = [column.status]
    include = [column.total]
    where   = "status <> 'closed'"
  }
  check "CK_acme_orders_total" {
    expr = "total >= 0"
  }
}
`

var _ = Describe("SQL Server HCL codec", func() {
	It("reads tables with identity, defaults, (max) types, clustering, included columns and filters", func() {
		var realm schema.Realm
		Expect(sqlserver.EvalHCLBytes([]byte(ordersHCL), &realm, nil)).To(Succeed())
		t, ok := realm.Schemas[0].Table("acme_orders")
		Expect(ok).To(BeTrue())

		id, _ := t.Column("id")
		Expect(id.Type.Type).To(Equal(&schema.IntegerType{T: "int"}))
		Expect(id.Attrs).To(ContainElement(&planner.Identity{Seed: 1, Increment: 1}))

		status, _ := t.Column("status")
		Expect(status.Type.Type).To(Equal(&schema.StringType{T: "nvarchar", Size: 20}))
		Expect(status.Default).To(Equal(&schema.Literal{V: "open"}))

		note, _ := t.Column("note")
		Expect(note.Type.Type).To(Equal(&schema.StringType{T: "nvarchar", Size: -1}))
		Expect(note.Type.Null).To(BeTrue())

		Expect(t.PrimaryKey.Attrs).To(ContainElement(&planner.Clustered{}))
		idx, _ := t.Index("IX_acme_orders_status")
		Expect(idx.Attrs).ToNot(ContainElement(&planner.Clustered{}))
		Expect(idx.Attrs).To(ContainElement(&planner.IndexInclude{Columns: []string{"total"}}))
		Expect(idx.Attrs).To(ContainElement(&planner.IndexPredicate{P: "status <> 'closed'"}))
	})

	DescribeTable("refuses what the driver does not manage",
		func(hcl, message string) {
			var realm schema.Realm
			err := sqlserver.EvalHCLBytes([]byte(hcl), &realm, nil)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a view", `schema "dbo" {}
view "acme_open" {
  schema = schema.dbo
  as     = "SELECT 1"
}`, "HCL view blocks are not supported"),
		Entry("a procedure block", `schema "dbo" {}
procedure "acme_purge" {
  schema = schema.dbo
  as     = "SELECT 1"
}`, "HCL procedure blocks are not supported"),
		Entry("a computed column", `schema "dbo" {}
table "acme_orders" {
  schema = schema.dbo
  column "total" {
    type = int
    as {
      expr = "1 + 1"
    }
  }
}`, `column "total": block "as" is not supported`),
		Entry("an alias type", `schema "dbo" {}
table "acme_orders" {
  schema = schema.dbo
  column "name" {
    type = sql("sysname")
  }
}`, `type "sysname" is not supported`),
	)
})
