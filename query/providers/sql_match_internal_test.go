// Specs for compiling a match filter to SQL: its clauses, and, over a sqlite
// corpus, the rows it selects compared one by one with collections.MatchItem.
package providers

import (
	"database/sql"
	"net/url"
	"strings"

	"github.com/flanksource/commons/collections"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

func match(field string, include, exclude []string) query.ColumnFilterValue {
	return query.ColumnFilterValue{Field: field, Kind: query.ColumnFilterKindMatch, Include: include, Exclude: exclude}
}

var _ = Describe("match filters in SQL", func() {
	DescribeTable("compiles each pattern into a case-insensitive comparison",
		func(filter query.ColumnFilterValue, clause string, args []any) {
			statement, bound, err := buildFilteredSQL(dialectPostgres, ordersQuery, []query.ColumnFilterValue{filter})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(clause))
			Expect(bound).To(Equal(args))
		},
		Entry("a whole value", match("name", []string{"Foo"}, nil), `LOWER("name") = $1`, []any{"foo"}),
		Entry("a prefix", match("name", []string{"Api*"}, nil), `LOWER("name") LIKE $1 ESCAPE '!'`, []any{"api%"}),
		Entry("a suffix", match("name", []string{"*-canary"}, nil), `LOWER("name") LIKE $1 ESCAPE '!'`, []any{"%-canary"}),
		Entry("a substring, its wildcards escaped", match("name", []string{"*50%*"}, nil), `LOWER("name") LIKE $1 ESCAPE '!'`, []any{"%50!%%"}),
		Entry("includes ORed", match("name", []string{"a*", "b*"}, nil),
			`(LOWER("name") LIKE $1 ESCAPE '!' OR LOWER("name") LIKE $2 ESCAPE '!')`, []any{"a%", "b%"}),
		Entry("an exclusion keeping rows with no value", match("name", nil, []string{"x*"}),
			`("name" IS NULL OR NOT (LOWER("name") LIKE $1 ESCAPE '!'))`, []any{"x%"}),
	)

	It("selects everything for a lone star, binding nothing", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery, []query.ColumnFilterValue{match("name", []string{"*"}, nil)})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring("1=1"))
		Expect(args).To(BeEmpty())
	})

	// The whole claim of the kind: SQL agrees with MatchItem on every row.
	Describe("over a sqlite corpus", func() {
		var database *sql.DB
		corpus := []string{
			"50%", "500", "a_b", "axb", "Foo", "foo", "foo-bar", "xylophone", "*", "a*b", "api-1", "api-1-canary", "API-2", "",
		}

		BeforeEach(func() {
			var err error
			database, err = sql.Open("sqlite", ":memory:")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(database.Close)
			database.SetMaxOpenConns(1)
			_, err = database.Exec(`CREATE TABLE names (value TEXT)`)
			Expect(err).ToNot(HaveOccurred())
			for _, value := range corpus {
				_, err = database.Exec(`INSERT INTO names (value) VALUES (?)`, value)
				Expect(err).ToNot(HaveOccurred())
			}
		})

		DescribeTable("selects the rows MatchItem matches",
			func(patterns ...string) {
				var include, exclude []string
				for _, pattern := range patterns {
					if excluded, ok := strings.CutPrefix(pattern, "!"); ok {
						exclude = append(exclude, excluded)
					} else {
						include = append(include, pattern)
					}
				}
				statement, args, err := buildFilteredSQL(dialectSQLite, "SELECT value FROM names", []query.ColumnFilterValue{match("value", include, exclude)})
				Expect(err).ToNot(HaveOccurred())
				rows, err := database.Query(statement, args...)
				Expect(err).ToNot(HaveOccurred())
				defer func() { Expect(rows.Close()).To(Succeed()) }()
				selected := map[string]bool{}
				for rows.Next() {
					var value string
					Expect(rows.Scan(&value)).To(Succeed())
					selected[value] = true
				}
				Expect(rows.Err()).ToNot(HaveOccurred())
				// MatchItem URL-decodes its patterns; a filter's are decoded
				// already, so the oracle is handed them escaped, to compare the
				// text the filter compares.
				escaped := make([]string, len(patterns))
				for index, pattern := range patterns {
					escaped[index] = url.QueryEscape(pattern)
				}
				for _, value := range corpus {
					matches, _ := collections.MatchItem(value, escaped...)
					Expect(selected[value]).To(Equal(matches), "value %q under %q", value, patterns)
				}
			},
			Entry("a literal percent", "50%"),
			Entry("a literal underscore", "a_b"),
			Entry("a value in another case", "foo"),
			Entry("a lone star", "*"),
			Entry("a star inside a value", "a*b"),
			Entry("a prefix in another case", "API*"),
			Entry("a suffix", "*-canary"),
			Entry("a substring holding an underscore", "*_*"),
			Entry("a substring holding a percent", "*%*"),
			Entry("an exclusion only", "!x*"),
			Entry("an exclusion of everything", "!*"),
			Entry("a prefix with an exclusion", "api*", "!*-canary"),
			Entry("two includes", "foo", "500"),
			Entry("an exclusion outweighing an include", "foo*", "!foo-bar"),
		)
	})
})
