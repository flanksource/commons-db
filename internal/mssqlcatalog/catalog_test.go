package mssqlcatalog_test

import (
	"database/sql/driver"

	"github.com/DATA-DOG/go-sqlmock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
)

var _ = Describe("SQL Server catalog queries", func() {
	const schema = "dbo"
	var (
		mock    sqlmock.Sqlmock
		queryer mssqlcatalog.Queryer
	)

	BeforeEach(func() {
		db, m, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			Expect(m.ExpectationsWereMet()).To(Succeed())
			m.ExpectClose()
			Expect(db.Close()).To(Succeed())
		})
		mock, queryer = m, mssqlcatalog.FromDB(db)
	})

	It("pushes the table-name filter into SQL as binds and maps default constraints and widths", func() {
		const policyTable, clientTable, defaultName, defaultDef = "AsPolicy", "AsClient", "DF_AsPolicy_Status", "('08')"
		mock.ExpectQuery(`WHERE c\.TABLE_SCHEMA = @p1 AND c\.TABLE_NAME IN \(@p2, @p3\) ORDER BY c\.TABLE_NAME, c\.ORDINAL_POSITION`).
			WithArgs(schema, policyTable, clientTable).
			WillReturnRows(sqlmock.NewRows([]string{
				"table", "column", "data_type", "nullable", "default", "position", "char_max", "num_precision", "num_scale",
				"pk", "uq", "identity", "max_length", "precision", "scale", "default_name",
				"seed", "increment", "computed", "user_defined",
			}).
				AddRow(policyTable, "Status", "nvarchar", 0, defaultDef, 2, 10, nil, nil, 0, 0, false, 20, 0, 0, defaultName, nil, nil, false, false).
				AddRow(policyTable, "PolicyID", "int", 0, nil, 1, nil, 10, 0, 1, 0, true, 4, 10, 0, nil, 1000, 5, false, false))

		columns, err := mssqlcatalog.Columns(GinkgoT().Context(), queryer, mssqlcatalog.Scope{
			Schema: schema, Names: []string{policyTable, clientTable},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(columns).To(Equal([]mssqlcatalog.Column{
			{
				Table: policyTable, Name: "Status", DataType: "nvarchar", Position: 2,
				Default: new(defaultDef), DefaultConstraint: new(defaultName),
				CharMaxLength: new(10), MaxLength: new(20), Precision: new(0), Scale: new(0),
			},
			{
				Table: policyTable, Name: "PolicyID", DataType: "int", Position: 1, PrimaryKey: true, Identity: true,
				NumericPrecision: new(10), NumericScale: new(0), MaxLength: new(4), Precision: new(10), Scale: new(0),
				IdentitySeed: new(int64(1000)), IdentityIncrement: new(int64(5)),
			},
		}))
	})

	It("reports partitioned tables and table triggers in the scope", func() {
		const ordersTable = "acme_orders"
		mock.ExpectQuery(`sys\.partition_schemes.*t\.name IN \(@p2\).*sys\.triggers.*t\.name IN \(@p4\)`).
			WithArgs(schema, ordersTable, schema, ordersTable).
			WillReturnRows(sqlmock.NewRows([]string{"table", "feature"}).
				AddRow(ordersTable, "partitioned").
				AddRow(ordersTable, "trigger tr_acme_orders_audit"))

		features, err := mssqlcatalog.UnmanagedFeatures(GinkgoT().Context(), queryer, mssqlcatalog.Scope{
			Schema: schema, Names: []string{ordersTable},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(features).To(Equal([]mssqlcatalog.TableFeature{
			{Table: ordersTable, Feature: "partitioned"},
			{Table: ordersTable, Feature: "trigger tr_acme_orders_audit"},
		}))
	})

	DescribeTable("reads routine definitions from sys.sql_modules, truncated only when a limit is set",
		func(limit int, definitionSQL string, args []driver.Value) {
			mock.ExpectQuery(definitionSQL + `.*WHERE s\.name = @p1 AND o\.type IN \('P', 'FN', 'IF', 'TF'\) ORDER BY o\.name`).
				WithArgs(args...).
				WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "definition", "return_type"}).
					AddRow(schema, "fn_age", "FN", "CREATE FUNCTION fn_age()", "int").
					AddRow(schema, "usp_purge", "P", nil, nil))

			routines, err := mssqlcatalog.Routines(GinkgoT().Context(), queryer, mssqlcatalog.Scope{Schema: schema, DefinitionLimit: limit})
			Expect(err).ToNot(HaveOccurred())
			Expect(routines).To(Equal([]mssqlcatalog.Routine{
				{Schema: schema, Name: "fn_age", Type: mssqlcatalog.ScalarFunction, Definition: new("CREATE FUNCTION fn_age()"), ReturnType: new("int")},
				{Schema: schema, Name: "usp_purge", Type: mssqlcatalog.Procedure},
			}))
		},
		Entry("full definition", 0, `RTRIM\(o\.type\), m\.definition,`, []driver.Value{schema}),
		Entry("bounded definition", 4096, `RTRIM\(o\.type\), LEFT\(m\.definition, @p2\),`, []driver.Value{schema, 4096}),
	)

	DescribeTable("rejects an invalid scope before querying",
		func(scope mssqlcatalog.Scope, message string) {
			_, err := mssqlcatalog.Columns(GinkgoT().Context(), queryer, scope)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("missing schema", mssqlcatalog.Scope{}, "schema is required"),
		Entry("blank name in the filter", mssqlcatalog.Scope{Schema: schema, Names: []string{"AsPolicy", " "}}, "name filter entry 1 is empty"),
		Entry("negative definition limit", mssqlcatalog.Scope{Schema: schema, DefinitionLimit: -1}, "definition limit must not be negative"),
	)

	It("fails loudly on a routine type outside the catalogued set", func() {
		mock.ExpectQuery(`FROM sys\.objects o`).
			WithArgs(schema).
			WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "definition", "return_type"}).
				AddRow(schema, "clr_proc", "PC", nil, nil))
		_, err := mssqlcatalog.Routines(GinkgoT().Context(), queryer, mssqlcatalog.Scope{Schema: schema})
		Expect(err).To(MatchError(ContainSubstring(`unexpected routine type "PC" for dbo.clr_proc`)))
	})
})
