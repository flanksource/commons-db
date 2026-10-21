package sqlserver_test

import (
	"ariga.io/atlas/sql/schema"

	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const countOrdersProc = `-- Counts a customer's orders.
CREATE PROCEDURE dbo.acme_count_orders
    @customer_id INT,
    @count INT OUTPUT
AS
BEGIN
    SET NOCOUNT ON;
    SET @count = (SELECT COUNT(*) FROM dbo.acme_orders WHERE customer_id = @customer_id);
END`

const orderTotalFunc = `CREATE OR ALTER FUNCTION acme_order_total(@order_id INT)
RETURNS DECIMAL(18, 2)
AS
BEGIN
    RETURN (SELECT SUM(quantity) FROM dbo.acme_order_lines WHERE order_id = @order_id);
END`

var _ = Describe("SQL Server routine definitions", func() {
	It("reads a procedure, its parameters and its definition into the schema", func() {
		s := schema.New("dbo")
		routine, err := planner.ParseRoutine(countOrdersProc)
		Expect(err).ToNot(HaveOccurred())
		Expect(routine.AddTo(s)).To(Succeed())

		Expect(s.Procs).To(HaveLen(1))
		proc := s.Procs[0]
		Expect(proc.Name).To(Equal("acme_count_orders"))
		Expect(proc.Schema).To(BeIdenticalTo(s))
		Expect(proc.Args).To(Equal([]*schema.FuncArg{
			{Name: "@customer_id", Type: &schema.IntegerType{T: "int"}, Mode: schema.FuncArgModeIn},
			{Name: "@count", Type: &schema.IntegerType{T: "int"}, Mode: schema.FuncArgModeInOut},
		}))
		Expect(proc.Attrs).To(ConsistOf(&planner.Definition{Text: countOrdersProc}))
	})

	It("reads a scalar function and its return type", func() {
		s := schema.New("dbo")
		routine, err := planner.ParseRoutine(orderTotalFunc)
		Expect(err).ToNot(HaveOccurred())
		Expect(routine.Name).To(Equal("acme_order_total"))
		Expect(routine.AddTo(s)).To(Succeed())
		Expect(s.Funcs).To(HaveLen(1))
		Expect(s.Funcs[0].Ret).To(Equal(&schema.DecimalType{T: "decimal", Precision: 18, Scale: 2}))
	})

	DescribeTable("refuses a definition that is not exactly one routine",
		func(definition, message string) {
			_, err := planner.ParseRoutine(definition)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a table", "CREATE TABLE dbo.acme_orders (id INT)", "makes no routine"),
		Entry("two routines", "CREATE PROCEDURE dbo.acme_one AS BEGIN SELECT 1; END\nCREATE PROCEDURE dbo.acme_two AS BEGIN SELECT 2; END", "sqlserver: parse routine"),
	)

	It("refuses a routine qualified with another schema", func() {
		routine, err := planner.ParseRoutine("CREATE PROCEDURE tenant_x.acme_purge AS BEGIN SELECT 1; END")
		Expect(err).ToNot(HaveOccurred())
		Expect(routine.AddTo(schema.New("dbo"))).To(MatchError(`sqlserver: routine tenant_x.acme_purge is declared in schema "tenant_x", not "dbo"`))
	})

	DescribeTable("CreateOrAlter turns a definition into CREATE OR ALTER, keeping every other byte",
		func(definition, expected string) {
			Expect(planner.CreateOrAlter(definition)).To(Equal(expected))
		},
		Entry("leading comments and a CREATE PROCEDURE",
			"-- note\n/* block */ create   PROCEDURE dbo.acme_x AS SELECT 1",
			"-- note\n/* block */ create OR ALTER   PROCEDURE dbo.acme_x AS SELECT 1"),
		Entry("already CREATE OR ALTER", "CREATE OR ALTER PROC dbo.acme_x AS SELECT 1", "CREATE OR ALTER PROC dbo.acme_x AS SELECT 1"),
		Entry("a unicode literal before the body", "CREATE FUNCTION dbo.acme_f() RETURNS NVARCHAR(4) AS BEGIN RETURN N'ünï'; END",
			"CREATE OR ALTER FUNCTION dbo.acme_f() RETURNS NVARCHAR(4) AS BEGIN RETURN N'ünï'; END"),
	)

	It("rewrites ALTER definitions from the live catalog", func() {
		Expect(planner.CreateOrAlter("ALTER PROCEDURE dbo.acme_x AS SELECT 1")).To(Equal("CREATE OR ALTER PROCEDURE dbo.acme_x AS SELECT 1"))
	})
})
