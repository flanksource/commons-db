package sqlserver_test

import (
	"github.com/DATA-DOG/go-sqlmock"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

var _ = Describe("SQL Server inspection", func() {
	var (
		mock   sqlmock.Sqlmock
		driver migrate.Driver
	)

	BeforeEach(func() {
		db, m, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			Expect(m.ExpectationsWereMet()).To(Succeed())
			m.ExpectClose()
			Expect(db.Close()).To(Succeed())
		})
		mock = m
		mock.ExpectQuery(`SERVERPROPERTY\('ProductVersion'\).*compatibility_level`).
			WillReturnRows(sqlmock.NewRows([]string{"version", "level"}).AddRow("16.0.4135.4", 100))
		driver, err = sqlserver.Open(db)
		Expect(err).ToNot(HaveOccurred())
		Expect(driver.(*sqlserver.Driver).CompatibilityLevel()).To(Equal(100))
	})

	expectSchema := func() {
		mock.ExpectQuery(`INFORMATION_SCHEMA\.SCHEMATA`).WithArgs("dbo").
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("dbo"))
	}
	expectRoutine := func(definition string) {
		mock.ExpectQuery(`FROM sys\.objects o.*o\.name IN \(@p2\)`).WithArgs("dbo", "acme_touch_order").
			WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "definition", "return_type"}).
				AddRow("dbo", "acme_touch_order", "P", definition, nil))
	}

	It("reads only the named routines, and no tables, when the mode asks for functions alone", func() {
		expectSchema()
		expectRoutine(touchOrderLive)
		s, err := driver.InspectSchema(GinkgoT().Context(), "dbo", &schema.InspectOptions{
			Mode: schema.InspectFuncs, Tables: []string{"acme_touch_order"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(s.Tables).To(BeEmpty())
		Expect(s.Procs).To(HaveLen(1))
		Expect(s.Procs[0].Attrs).To(ConsistOf(&planner.Definition{Text: touchOrderLive}))
	})

	It("fails on a live definition that does not parse, naming the routine", func() {
		expectSchema()
		expectRoutine("CREATE PROCEDURE dbo.acme_touch_order AS BEGIN")
		_, err := driver.InspectSchema(GinkgoT().Context(), "dbo", &schema.InspectOptions{
			Mode: schema.InspectFuncs, Tables: []string{"acme_touch_order"},
		})
		Expect(err).To(MatchError(ContainSubstring("sqlserver: live routine dbo.acme_touch_order: sqlserver: parse routine")))
	})

	It("refuses to inspect views", func() {
		_, err := driver.InspectSchema(GinkgoT().Context(), "dbo", &schema.InspectOptions{Mode: schema.InspectViews})
		Expect(err).To(MatchError(ContainSubstring("views, triggers, types and objects are not supported by the SQL Server driver")))
	})
})
