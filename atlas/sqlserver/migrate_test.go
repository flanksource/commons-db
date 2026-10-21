package sqlserver_test

import (
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

func planCmds(changes ...schema.Change) []string {
	plan, err := planner.DefaultPlan.PlanChanges(GinkgoT().Context(), "golden", changes)
	Expect(err).ToNot(HaveOccurred())
	cmds := make([]string, len(plan.Changes))
	for i, c := range plan.Changes {
		cmds[i] = c.Cmd
	}
	return cmds
}

// declaredOrders is acme_orders as ordersHCL declares it.
func declaredOrders() *schema.Table {
	var realm schema.Realm
	Expect(sqlserver.EvalHCLBytes([]byte(ordersHCL), &realm, nil)).To(Succeed())
	t, ok := realm.Schemas[0].Table("acme_orders")
	Expect(ok).To(BeTrue())
	return t
}

func modify(t *schema.Table, changes ...schema.Change) *schema.ModifyTable {
	return &schema.ModifyTable{T: t, Changes: changes}
}

var _ = Describe("SQL Server plan", func() {
	It("creates a table with identity, named default, clustered key and check, then its filtered covering index", func() {
		Expect(planCmds(&schema.AddTable{T: declaredOrders()})).To(Equal([]string{
			"CREATE TABLE [dbo].[acme_orders] ([id] int IDENTITY(1,1) NOT NULL, " +
				"[status] nvarchar(20) NOT NULL CONSTRAINT [DF_acme_orders_status] DEFAULT N'open', " +
				"[note] nvarchar(max) NULL, [total] decimal(18,2) NOT NULL, " +
				"PRIMARY KEY CLUSTERED ([id]), CONSTRAINT [CK_acme_orders_total] CHECK (total >= 0))",
			"CREATE NONCLUSTERED INDEX [IX_acme_orders_status] ON [dbo].[acme_orders] ([status]) " +
				"INCLUDE ([total]) WHERE status <> 'closed'",
		}))
	})

	It("adds a column with its default", func() {
		t := declaredOrders()
		priority := schema.NewIntColumn("priority", "int").SetDefault(&schema.Literal{V: "0"})
		Expect(planCmds(modify(t, &schema.AddColumn{C: priority}))).To(Equal([]string{
			"ALTER TABLE [dbo].[acme_orders] ADD [priority] int NOT NULL CONSTRAINT [DF_acme_orders_priority] DEFAULT 0",
		}))
	})

	It("rebinds a live default around a type change", func() {
		t := declaredOrders()
		live := schema.NewColumn("status").SetType(&schema.StringType{T: "nvarchar", Size: 10}).
			SetDefault(&schema.RawExpr{X: "(N'new')"}).AddAttrs(&planner.DefaultConstraint{Name: "DF__acme_orde__statu__1A2B"})
		declared, _ := t.Column("status")
		Expect(planCmds(modify(t, &schema.ModifyColumn{From: live, To: declared, Change: schema.ChangeType | schema.ChangeDefault}))).To(Equal([]string{
			"ALTER TABLE [dbo].[acme_orders] DROP CONSTRAINT [DF__acme_orde__statu__1A2B]",
			"ALTER TABLE [dbo].[acme_orders] ALTER COLUMN [status] nvarchar(20) NOT NULL",
			"ALTER TABLE [dbo].[acme_orders] ADD CONSTRAINT [DF_acme_orders_status] DEFAULT N'open' FOR [status]",
		}))
	})

	It("changes nullability alone with ALTER COLUMN", func() {
		t := declaredOrders()
		declared, _ := t.Column("note")
		live := schema.NewColumn("note").SetType(declared.Type.Type)
		Expect(planCmds(modify(t, &schema.ModifyColumn{From: live, To: declared, Change: schema.ChangeNull}))).To(Equal([]string{
			"ALTER TABLE [dbo].[acme_orders] ALTER COLUMN [note] nvarchar(max) NULL",
		}))
	})

	It("adds a unique clustered index and drops a constraint-backed unique index", func() {
		t := declaredOrders()
		status, _ := t.Column("status")
		added := schema.NewUniqueIndex("UX_acme_orders_status").AddColumns(status).AddAttrs(&planner.Clustered{})
		dropped := schema.NewUniqueIndex("UQ_acme_orders_note").AddAttrs(&planner.UniqueConstraint{})
		Expect(planCmds(modify(t, &schema.AddIndex{I: added}, &schema.DropIndex{I: dropped}))).To(Equal([]string{
			"CREATE UNIQUE CLUSTERED INDEX [UX_acme_orders_status] ON [dbo].[acme_orders] ([status])",
			"ALTER TABLE [dbo].[acme_orders] DROP CONSTRAINT [UQ_acme_orders_note]",
		}))
	})

	It("replaces a check on the referenced table before adding the foreign key to it", func() {
		t := declaredOrders()
		lines := schema.NewTable("acme_order_lines").AddColumns(schema.NewIntColumn("order_id", "int"))
		t.Schema.AddTables(lines)
		orderID, _ := lines.Column("order_id")
		id, _ := t.Column("id")
		fk := schema.NewForeignKey("FK_acme_order_lines_order").AddColumns(orderID).SetRefTable(t).AddRefColumns(id).
			SetOnDelete(schema.Cascade)
		lines.AddForeignKeys(fk)
		from := &schema.Check{Name: "CK_acme_orders_total", Expr: "([total]>=(0))"}
		to := &schema.Check{Name: "CK_acme_orders_total", Expr: "total > 0"}
		Expect(planCmds(modify(lines, &schema.AddForeignKey{F: fk}), modify(t, &schema.ModifyCheck{From: from, To: to}))).To(Equal([]string{
			"ALTER TABLE [dbo].[acme_orders] DROP CONSTRAINT [CK_acme_orders_total]",
			"ALTER TABLE [dbo].[acme_orders] ADD CONSTRAINT [CK_acme_orders_total] CHECK (total > 0)",
			"ALTER TABLE [dbo].[acme_order_lines] ADD CONSTRAINT [FK_acme_order_lines_order] FOREIGN KEY ([order_id]) " +
				"REFERENCES [dbo].[acme_orders] ([id]) ON DELETE CASCADE",
		}))
	})

	It("runs a declared routine as CREATE OR ALTER, byte for byte, and drops by name", func() {
		declared := routineSchema(countOrdersProc, orderTotalFunc)
		live := routineSchema(touchOrderLive)
		Expect(planCmds(
			&schema.AddProc{P: declared.Procs[0]},
			&schema.ModifyFunc{From: declared.Funcs[0], To: declared.Funcs[0]},
			&schema.DropProc{P: live.Procs[0]},
		)).To(Equal([]string{
			"-- Counts a customer's orders.\nCREATE OR ALTER PROCEDURE dbo.acme_count_orders\n    @customer_id INT,\n" +
				"    @count INT OUTPUT\nAS\nBEGIN\n    SET NOCOUNT ON;\n" +
				"    SET @count = (SELECT COUNT(*) FROM dbo.acme_orders WHERE customer_id = @customer_id);\nEND",
			orderTotalFunc,
			"DROP PROCEDURE [dbo].[acme_touch_order]",
		}))
	})

	It("refuses a view without its complete T-SQL definition", func() {
		_, err := planner.DefaultPlan.PlanChanges(GinkgoT().Context(), "golden",
			[]schema.Change{&schema.AddView{V: schema.NewView("acme_open", "SELECT 1")}}, func(*migrate.PlanOptions) {})
		Expect(err).To(MatchError("sqlserver: routine acme_open has no T-SQL definition"))
	})
})
