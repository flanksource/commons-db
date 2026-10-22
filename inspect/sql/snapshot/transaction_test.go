package snapshot

import (
	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Transaction-bound migration snapshots", func() {
	It("uses the supplied transaction for permission and catalog reads", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectBegin()
		tx, err := db.BeginTx(GinkgoT().Context(), nil)
		Expect(err).NotTo(HaveOccurred())
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow("dbo"))
		mock.ExpectQuery(`OBJECT_DEFINITION\(o.object_id\)`).WithArgs("dbo", "").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		for _, query := range []string{`FROM INFORMATION_SCHEMA.COLUMNS`, `FROM sys.indexes`, `FROM sys.index_columns`, `FROM sys.foreign_keys`, `FROM sys.check_constraints`, `FROM sys.objects o`, `FROM sys.columns c`, `SCHEMA_NAME\(trigger_object.schema_id\)`, `FROM sys.sql_expression_dependencies`, `SELECT object_name, feature`} {
			mock.ExpectQuery(query).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		}
		result, err := Inspect(GinkgoT().Context(), tx, Options{Schemas: []string{"dbo"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Schemas).To(Equal([]string{"dbo"}))
		Expect(result.Schema.Tables).To(BeEmpty())
		mock.ExpectRollback()
		Expect(tx.Rollback()).To(Succeed())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
