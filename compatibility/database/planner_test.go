package database_test

import (
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	oldatlas "github.com/flanksource/commons-db/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	_ *planner.Identity          = new(oldatlas.Identity)
	_ *planner.DefaultConstraint = new(oldatlas.DefaultConstraint)
	_ *planner.Clustered         = new(oldatlas.Clustered)
	_ *planner.IndexInclude      = new(oldatlas.IndexInclude)
	_ *planner.IndexPredicate    = new(oldatlas.IndexPredicate)
	_ *planner.UniqueConstraint  = new(oldatlas.UniqueConstraint)
	_ *planner.SpecialType       = new(oldatlas.SpecialType)
	_ *planner.Definition        = new(oldatlas.Definition)
	_ *planner.Routine           = new(oldatlas.Routine)
	_ schema.Differ              = oldatlas.DefaultDiff
	_ migrate.PlanApplier        = oldatlas.DefaultPlan
)

var _ = Describe("original SQL Server planner API", func() {
	It("retains type parsing, routine conversion and planning through the commons package", func(ctx SpecContext) {
		typ, err := oldatlas.ParseType("nvarchar(20)")
		Expect(err).NotTo(HaveOccurred())
		formatted, err := oldatlas.FormatType(typ)
		Expect(err).NotTo(HaveOccurred())
		Expect(formatted).To(Equal("nvarchar(20)"))
		Expect(oldatlas.Dialect).To(Equal("tsql"))
		const definition = "CREATE PROCEDURE dbo.example AS SELECT 1"
		routine, err := oldatlas.ParseRoutine(definition)
		Expect(err).NotTo(HaveOccurred())
		declared := schema.New("dbo")
		Expect(routine.AddTo(declared)).To(Succeed())
		converted, err := oldatlas.CreateOrAlter(definition)
		Expect(err).NotTo(HaveOccurred())
		Expect(converted).To(Equal("CREATE OR ALTER PROCEDURE dbo.example AS SELECT 1"))
		plan, err := oldatlas.DefaultPlan.PlanChanges(ctx, "compatibility", []schema.Change{&schema.AddProc{P: declared.Procs[0]}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(HaveLen(1))
		Expect(plan.Changes[0].Cmd).To(Equal(converted))
	})
})
