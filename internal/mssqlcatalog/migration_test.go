package mssqlcatalog_test

import (
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Migration catalogue row integrity", func() {
	It("returns schema, index-column and foreign-key values after scanning", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		q := mssqlcatalog.FromDB(db)
		ctx, scope := GinkgoT().Context(), mssqlcatalog.Scope{Schema: "dbo"}
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow("dbo"))
		schemas, err := mssqlcatalog.Schemas(ctx, q, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(schemas).To(Equal([]string{"dbo"}))
		mock.ExpectQuery("FROM sys.index_columns").WillReturnRows(sqlmock.NewRows([]string{"table", "index", "column", "ordinal", "included", "descending"}).AddRow("orders", "IX_total", "total", 1, false, true))
		parts, err := mssqlcatalog.IndexColumns(ctx, q, scope)
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]mssqlcatalog.IndexColumn{{Table: "orders", Index: "IX_total", Column: "total", KeyOrdinal: 1, Descending: true}}))
		mock.ExpectQuery("FROM sys.foreign_keys").WillReturnRows(sqlmock.NewRows([]string{"table", "name", "column", "refSchema", "refTable", "refColumn", "delete", "update"}).AddRow("orders", "FK_tenant", "tenant_id", "tenant", "accounts", "id", "CASCADE", "NO_ACTION"))
		keys, err := mssqlcatalog.ForeignKeys(ctx, q, scope)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(Equal([]mssqlcatalog.ForeignKey{{Table: "orders", Name: "FK_tenant", Column: "tenant_id", RefSchema: "tenant", RefTable: "accounts", RefColumn: "id", OnDelete: "CASCADE", OnUpdate: "NO_ACTION"}}))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
