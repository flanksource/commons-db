package snapshot

import (
	"testing"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSnapshot(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server migration snapshots")
}

func fixture() catalog {
	return catalog{schema: "dbo", tables: []mssqlcatalog.Table{{Schema: "dbo", Name: "orders", Type: "BASE TABLE"}},
		columns: []mssqlcatalog.Column{
			{Table: "orders", Name: "id", DataType: "int", MaxLength: new(4), Precision: new(10), Scale: new(0), Identity: true, IdentitySeed: new(int64(100)), IdentityIncrement: new(int64(5))},
			{Table: "orders", Name: "label", DataType: "nvarchar", MaxLength: new(40), Precision: new(0), Scale: new(0), Default: new("(N'pending')"), DefaultConstraint: new("DF_label")},
		}, indexes: []mssqlcatalog.Index{{Table: "orders", Name: "IX_label", TypeDesc: "NONCLUSTERED", Filter: "[label] IS NOT NULL"}},
		parts:      []mssqlcatalog.IndexColumn{{Table: "orders", Index: "IX_label", Column: "label", KeyOrdinal: 1, Descending: true}, {Table: "orders", Index: "IX_label", Column: "id", Included: true}},
		collations: map[[2]string]string{{"orders", "label"}: "Latin1_General_CI_AS"},
	}
}

var _ = Describe("Complete catalogue projection", func() {
	It("preserves identity, named defaults, collation and index parts", func() {
		schema, err := build(fixture())
		Expect(err).NotTo(HaveOccurred())
		Expect(schema.Tables).To(Equal([]migrate.Table{{Name: migrate.Name{Schema: "dbo", Name: "orders"},
			Columns: []migrate.Column{{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 100, Increment: 5}}, {Name: "label", Type: "nvarchar(20)", Default: &migrate.Default{Name: "DF_label", Expression: "(N'pending')"}, Collation: "Latin1_General_CI_AS"}},
			Indexes: []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label", Descending: true}}, Included: []string{"id"}, Filter: "[label] IS NOT NULL"}},
		}}))
	})

	DescribeTable("refuses definitions that cannot be reproduced",
		func(change func(*catalog), message string) {
			c := fixture()
			change(&c)
			_, err := build(c)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("computed column", func(c *catalog) { c.columns[0].Computed = true }, "computed"),
		Entry("user-defined type", func(c *catalog) { c.columns[0].UserDefinedType = true }, "user-defined"),
		Entry("missing identity seed", func(c *catalog) { c.columns[0].IdentitySeed = nil }, "identity"),
		Entry("unnamed default", func(c *catalog) { c.columns[1].DefaultConstraint = nil }, "constraint name"),
		Entry("disabled check", func(c *catalog) {
			c.checks = []mssqlcatalog.CheckConstraint{{Table: "orders", Name: "CK_id", Definition: "id > 0", Disabled: true}}
		}, "disabled check"),
		Entry("encrypted routine", func(c *catalog) {
			c.routines = []mssqlcatalog.Routine{{Schema: "dbo", Name: "update_orders", Type: mssqlcatalog.Procedure}}
		}, "definition"),
	)
})
