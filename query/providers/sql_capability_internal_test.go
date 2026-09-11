package providers

import (
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL array capability enforcement", func() {
	It("does not probe for a scalar-only operation", func() {
		Expect(requireSQLArrayFilters(context.New(), nil, dialectPostgres, false)).To(Succeed())
	})

	It("probes and requires array support exactly once", func() {
		db, mock, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta(
			`SELECT current_database(), current_setting('server_version'), current_setting('server_version_num')::integer`,
		)).WillReturnRows(sqlmock.NewRows([]string{"database", "version", "version_num"}).
			AddRow("warehouse", "17.4", 170004))

		Expect(requireSQLArrayFilters(context.New(), db, dialectPostgres, true)).To(Succeed())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("rejects a backend below its required array version", func() {
		db, mock, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT COALESCE(DATABASE(), ''), VERSION()`)).
			WillReturnRows(sqlmock.NewRows([]string{"database", "version"}).AddRow("warehouse", "8.0.3"))

		Expect(requireSQLArrayFilters(context.New(), db, dialectMySQL, true)).To(
			MatchError(ContainSubstring("version 8.0.4 or newer")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	DescribeTable("recognizes only active array predicates",
		func(filters []query.ColumnFilterValue, expected bool) {
			Expect(filtersUseArrays(filters)).To(Equal(expected))
		},
		Entry("no filters", nil, false),
		Entry("a scalar filter", []query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, false),
		Entry("an empty array filter", []query.ColumnFilterValue{{
			Field: "tables", Kind: query.ColumnFilterKindTerms, Array: true,
		}}, false),
		Entry("an active array filter", []query.ColumnFilterValue{{
			Field: "tables", Kind: query.ColumnFilterKindTerms, Array: true, Include: []string{"policy"},
		}}, true),
	)
})
