package snapshot

import (
	"errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Migration inspection boundary", func() {
	It("rejects expression dependencies that the table migration model cannot preserve", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery(`dependent default, check or column expression`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"object", "feature"}).AddRow("DF_orders_id", "dependent default, check or column expression"))
		err = refuseUnsupported(GinkgoT().Context(), db, "dbo")
		Expect(err).To(MatchError(And(ContainSubstring("dbo.DF_orders_id"), ContainSubstring("migration snapshot is incomplete"))))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("requires full definition visibility before enumerating objects", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(0))
		_, err = Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).To(MatchError(ContainSubstring("VIEW DEFINITION")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("reads full view definitions and trigger state without explorer limits", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow("dbo"))
		viewSQL := "CREATE VIEW dbo.orders AS SELECT 1 AS id"
		triggerSQL := "CREATE TRIGGER dbo.audit_orders ON dbo.orders INSTEAD OF INSERT AS SELECT 1"
		mock.ExpectQuery(`OBJECT_DEFINITION\(o.object_id\)`).WithArgs("dbo", "").WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "definition"}).AddRow("dbo", "orders", "VIEW", viewSQL))
		for _, query := range []string{`FROM INFORMATION_SCHEMA.COLUMNS`, `FROM sys.indexes`, `FROM sys.index_columns`, `FROM sys.foreign_keys`, `FROM sys.check_constraints`, `FROM sys.objects o`, `FROM sys.columns c`} {
			mock.ExpectQuery(query).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		}
		mock.ExpectQuery(`SCHEMA_NAME\(trigger_object.schema_id\)`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "parentSchema", "parent", "disabled", "definition"}).AddRow("dbo", "audit_orders", "dbo", "orders", true, triggerSQL))
		mock.ExpectQuery(`FROM sys.sql_expression_dependencies`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		mock.ExpectQuery(`SELECT object_name, feature`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		result, err := Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Schema.Definitions).To(Equal([]migrate.Definition{
			{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Kind: "VIEW", SQL: viewSQL},
			{Name: migrate.Name{Schema: "dbo", Name: "audit_orders"}, Kind: "TRIGGER", SQL: triggerSQL, Parent: &migrate.Name{Schema: "dbo", Name: "orders"}, Disabled: true},
		}))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("propagates catalogue read failures with their schema", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WillReturnError(errors.New("catalogue unavailable"))
		_, err = Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).To(MatchError(And(ContainSubstring("dbo"), ContainSubstring("catalogue unavailable"))))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
