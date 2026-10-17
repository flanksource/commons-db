package sqlserver_test

import (
	"fmt"

	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/atlas/sqlserver"
)

const touchOrderLive = `-- Marks an order as touched.
CREATE PROCEDURE dbo.acme_touch_order
    @order_id INT -- the order
AS
BEGIN
    /* one statement */
    UPDATE dbo.acme_orders SET touched_at = SYSUTCDATETIME(), label = N'touched' WHERE order_id = @order_id;
END`

func routineSchema(definitions ...string) *schema.Schema {
	s := schema.New("dbo")
	for _, definition := range definitions {
		routine, err := sqlserver.ParseRoutine(definition)
		Expect(err).ToNot(HaveOccurred())
		Expect(routine.AddTo(s)).To(Succeed())
	}
	return s
}

var _ = Describe("SQL Server routine diff", func() {
	DescribeTable("compares routine definitions by their canonical form",
		func(declared string, expected types) {
			from, to := routineSchema(touchOrderLive), routineSchema(declared)
			changes, err := sqlserver.DefaultDiff.SchemaDiff(from, to)
			Expect(err).ToNot(HaveOccurred())
			Expect(changeTypes(changes)).To(Equal(expected))
		},
		Entry("the same text", touchOrderLive, types{}),
		Entry("comments, case, whitespace, brackets and OR ALTER differ",
			"create or alter procedure [dbo].[acme_touch_order] @order_id int as begin update dbo.acme_orders set touched_at = sysutcdatetime(), label = N'touched' where order_id = @order_id; end",
			types{}),
		Entry("a string literal differs",
			"CREATE PROCEDURE dbo.acme_touch_order @order_id INT AS BEGIN UPDATE dbo.acme_orders SET touched_at = SYSUTCDATETIME(), label = N'Touched' WHERE order_id = @order_id; END",
			types{"*schema.ModifyProc"}),
		Entry("a statement is added",
			"CREATE PROCEDURE dbo.acme_touch_order @order_id INT AS BEGIN SET NOCOUNT ON; UPDATE dbo.acme_orders SET touched_at = SYSUTCDATETIME(), label = N'touched' WHERE order_id = @order_id; END",
			types{"*schema.ModifyProc"}),
	)

	It("adds declared routines the database lacks and drops routines only the database has", func() {
		from := routineSchema(touchOrderLive)
		to := routineSchema(countOrdersProc, orderTotalFunc)
		changes, err := sqlserver.DefaultDiff.SchemaDiff(from, to)
		Expect(err).ToNot(HaveOccurred())
		Expect(changeTypes(changes)).To(Equal(types{"*schema.AddProc", "*schema.AddFunc", "*schema.DropProc"}))
	})

	It("refuses to turn a procedure into a function", func() {
		from := routineSchema("CREATE PROCEDURE dbo.acme_order_total AS BEGIN SELECT 1; END")
		_, err := sqlserver.DefaultDiff.SchemaDiff(from, routineSchema(orderTotalFunc))
		Expect(err).To(MatchError(ContainSubstring("acme_order_total is a procedure in the database and a function in the bundle")))
	})

	It("fails on a live definition sqlglot cannot read instead of assuming a change", func() {
		from := routineSchema(touchOrderLive)
		from.Procs[0].Attrs = []schema.Attr{&sqlserver.Definition{Text: "CREATE PROCEDURE dbo.acme_touch_order AS BEGIN"}}
		_, err := sqlserver.DefaultDiff.SchemaDiff(from, routineSchema(touchOrderLive))
		Expect(err).To(MatchError(ContainSubstring("live definition: sqlserver: routine acme_touch_order")))
	})
})

var _ = Describe("SQL Server table diff", func() {
	DescribeTable("compares column defaults as SQL Server stores them",
		func(live, declared schema.Expr, expected types) {
			changes, err := sqlserver.DefaultDiff.TableDiff(ordersTable(live, nil), ordersTable(declared, nil))
			Expect(err).ToNot(HaveOccurred())
			Expect(changeTypes(changes)).To(Equal(expected))
		},
		Entry("a number wrapped twice", &schema.RawExpr{X: "((0))"}, &schema.Literal{V: "0"}, types{}),
		Entry("a function call in another case", &schema.RawExpr{X: "(getdate())"}, &schema.RawExpr{X: "GETDATE()"}, types{}),
		Entry("a string literal", &schema.RawExpr{X: "('open')"}, &schema.Literal{V: "'open'"}, types{}),
		Entry("a different number", &schema.RawExpr{X: "((0))"}, &schema.Literal{V: "1"}, types{"*schema.ModifyColumn"}),
		Entry("a removed default", &schema.RawExpr{X: "((0))"}, nil, types{"*schema.ModifyColumn"}),
	)

	It("matches an unquoted HCL string default to the N'...' literal SQL Server stores for nvarchar", func() {
		column := func(def schema.Expr) *schema.Table {
			c := schema.NewStringColumn("status", "nvarchar")
			c.Type.Type = &schema.StringType{T: "nvarchar", Size: 20}
			c.Default = def
			t := schema.NewTable("acme_orders").AddColumns(c)
			schema.New("dbo").AddTables(t)
			return t
		}
		changes, err := sqlserver.DefaultDiff.TableDiff(column(&schema.RawExpr{X: "(N'open')"}), column(&schema.Literal{V: "open"}))
		Expect(err).ToNot(HaveOccurred())
		Expect(changes).To(BeEmpty())
	})

	DescribeTable("compares check constraints by their canonical predicate",
		func(live, declared string, expected types) {
			changes, err := sqlserver.DefaultDiff.TableDiff(ordersTable(nil, &schema.Check{Name: "CK_acme_orders_qty", Expr: live}),
				ordersTable(nil, &schema.Check{Name: "CK_acme_orders_qty", Expr: declared}))
			Expect(err).ToNot(HaveOccurred())
			Expect(changeTypes(changes)).To(Equal(expected))
		},
		Entry("stored with brackets and parentheses", "([qty]>(0))", "qty > 0", types{}),
		Entry("grouping that changes the meaning", "(([qty]+[bonus])*(2)>(10))", "qty + bonus * 2 > 10", types{"*schema.ModifyCheck"}),
	)
})

type types []string

func changeTypes(changes []schema.Change) types {
	out := types{}
	for _, change := range changes {
		if modify, ok := change.(*schema.ModifyTable); ok {
			out = append(out, changeTypes(modify.Changes)...)
			continue
		}
		out = append(out, fmt.Sprintf("%T", change))
	}
	return out
}

func ordersTable(def schema.Expr, check *schema.Check) *schema.Table {
	column := schema.NewIntColumn("qty", "int")
	column.Default = def
	t := schema.NewTable("acme_orders").AddColumns(column)
	if check != nil {
		t.AddChecks(check)
	}
	schema.New("dbo").AddTables(t)
	return t
}
