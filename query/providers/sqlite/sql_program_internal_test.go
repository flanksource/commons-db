package sqlite

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL program classification", func() {
	DescribeTable("decides which statements run as programs",
		func(dialect sqlDialect, statement string, program bool) {
			_, got, err := sqlProgram(dialect, statement)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(program))
		},
		Entry("a SELECT pages", dialectSQLite, "SELECT 1 AS one", false),
		Entry("a UNION pages", dialectSQLServer, "SELECT 1 AS one UNION ALL SELECT 2", false),
		Entry("a CTE query pages", dialectPostgres, "WITH t AS (SELECT 1 AS one) SELECT * FROM t", false),
		Entry("SELECT INTO writes", dialectSQLServer, "SELECT * INTO copy FROM accounts", true),
		Entry("EXEC runs", dialectSQLServer, "EXEC dbo.mc_DetectLoopingSpawn @MinGenerations = 3, @Stage = 1", true),
		Entry("DELETE runs", dialectSQLite, "DELETE FROM accounts", true),
		Entry("two statements run", dialectSQLite, "SELECT 1; SELECT 2", true),
		Entry("SET then SELECT runs", dialectSQLServer, "SET NOCOUNT ON; SELECT 1", true),
		Entry("a GO script runs", dialectSQLServer, "SELECT 1\nGO\n", true),
	)

	It("binds a param inside a T-SQL EXEC as a driver arg", func() {
		parameterized, err := (sqlProvider{connType: string(dialectSQLServer)}).ParameterizeQuery(query.QueryParameterizationRequest{
			Query:  "EXEC dbo.mc_ExecuteActivityPurge @ExpectedActivities = $(.params.expected), @Note = 'keep $ as text'",
			Params: map[string]any{"expected": 42},
		})
		Expect(err).ToNot(HaveOccurred())
		batches, program, err := sqlProgram(dialectSQLServer, parameterized.Query)
		Expect(err).ToNot(HaveOccurred())
		Expect(program).To(BeTrue())
		text, args, err := bindSQLBatch(dialectSQLServer, batches[0], sqlQuery{Text: parameterized.Query, Args: parameterized.Args})
		Expect(err).ToNot(HaveOccurred())
		Expect(text).To(Equal("EXEC dbo.mc_ExecuteActivityPurge @ExpectedActivities = @p1, @Note = 'keep $ as text'"))
		Expect(args).To(Equal([]any{42}))
	})

	It("refuses a param marker quoted inside an EXEC", func() {
		_, err := (sqlProvider{connType: string(dialectSQLServer)}).ParameterizeQuery(query.QueryParameterizationRequest{
			Query:  "EXEC dbo.proc @Note = '$(.params.note)'",
			Params: map[string]any{"note": "x"},
		})
		Expect(err).To(MatchError(ContainSubstring("must not be quoted or commented")))
	})

	It("never rewrites a routine definition", func() {
		parameterized, err := (sqlProvider{connType: string(dialectSQLServer)}).ParameterizeQuery(query.QueryParameterizationRequest{
			Query:  "CREATE OR ALTER PROCEDURE dbo.p AS SELECT $(.params.x) AS x\nGO\nEXEC dbo.p",
			Params: map[string]any{"x": 1},
		})
		Expect(err).ToNot(HaveOccurred())
		batches, _, err := sqlProgram(dialectSQLServer, parameterized.Query)
		Expect(err).ToNot(HaveOccurred())
		_, _, err = bindSQLBatch(dialectSQLServer, batches[0], sqlQuery{Text: parameterized.Query, Args: parameterized.Args})
		Expect(err).To(MatchError(ContainSubstring("cannot be bound inside it")))
	})

	It("refuses GO 0", func() {
		_, _, err := splitSQLBatches(dialectSQLServer, "SELECT 1\nGO 0\n")
		Expect(err).To(MatchError(ContainSubstring("line 2: GO 0 must repeat its batch a positive number of times")))
	})
})
