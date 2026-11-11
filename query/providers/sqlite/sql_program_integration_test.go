package sqlite_test

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// accountsDB is a file database seeded with three accounts, so a program's
// writes can be read back by a separate connection.
func accountsDB() string {
	GinkgoHelper()
	path := filepath.Join(GinkgoT().TempDir(), "accounts.sqlite")
	seed, err := sql.Open("sqlite", path)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = seed.Close() }()
	_, err = seed.Exec(`CREATE TABLE accounts(id INTEGER PRIMARY KEY, owner TEXT, balance INTEGER);
		INSERT INTO accounts VALUES (1, 'ann', 10), (2, 'bob', 20), (3, 'cy', 30)`)
	Expect(err).ToNot(HaveOccurred())
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func programProfile(dsn, statement string) query.Profile {
	return query.Profile{
		Name:     "program",
		Provider: query.ProviderConfig{Type: "sqlite", Options: map[string]any{"url": dsn}},
		Query:    statement,
		Params: []query.ParamDef{
			{Name: "id", Type: query.ParamTypeNumber},
			{Name: "amount", Type: query.ParamTypeNumber},
		},
	}
}

func balances(dsn string) map[int64]int64 {
	GinkgoHelper()
	client, err := sql.Open("sqlite", dsn)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = client.Close() }()
	rows, err := client.Query("SELECT id, balance FROM accounts ORDER BY id")
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = rows.Close() }()
	found := map[int64]int64{}
	for rows.Next() {
		var id, balance int64
		Expect(rows.Scan(&id, &balance)).To(Succeed())
		found[id] = balance
	}
	Expect(rows.Err()).ToNot(HaveOccurred())
	return found
}

func run(dsn, statement string, params map[string]any) *query.Result {
	GinkgoHelper()
	result, err := query.Execute(context.New(), programProfile(dsn, statement), params)
	Expect(err).ToNot(HaveOccurred())
	return result
}

var _ = Describe("sqlite SQL programs", func() {
	DescribeTable("runs a write with bound params and reports what it changed",
		func(statement string, want map[int64]int64, affected int64) {
			dsn := accountsDB()
			result := run(dsn, statement, map[string]any{"id": 2, "amount": 5})
			Expect(balances(dsn)).To(Equal(want))
			Expect(result.Rows).To(BeEmpty())
			Expect(result.Batches).To(HaveLen(1))
			Expect(result.Batches[0].RowsAffected).To(HaveValue(Equal(affected)))
		},
		Entry("UPDATE", "UPDATE accounts SET balance = balance + $(.params.amount) WHERE id >= {{.params.id}}",
			map[int64]int64{1: 10, 2: 25, 3: 35}, int64(2)),
		Entry("DELETE", "DELETE FROM accounts WHERE id = $(.params.id)",
			map[int64]int64{1: 10, 3: 30}, int64(1)),
		Entry("INSERT", "INSERT INTO accounts VALUES ($(.params.id) + 10, 'dee', $(.params.amount))",
			map[int64]int64{1: 10, 2: 20, 3: 30, 12: 5}, int64(1)),
	)

	It("returns the rows of a statement that reads after it writes", func() {
		dsn := accountsDB()
		result := run(dsn, "UPDATE accounts SET balance = 0 WHERE id = $(.params.id) RETURNING id, balance",
			map[string]any{"id": 3, "amount": 0})
		Expect(result.Rows).To(Equal([]query.Row{{"id": int64(3), "balance": int64(0)}}))
		Expect(result.Batches[0].RowsAffected).To(HaveValue(Equal(int64(1))))
	})

	It("binds params in every statement of a multi-statement batch", func() {
		dsn := accountsDB()
		result := run(dsn, `UPDATE accounts SET balance = $(.params.amount) WHERE id = 1;
			SELECT owner, balance FROM accounts WHERE id = $(.params.id)`, map[string]any{"id": 2, "amount": 99})
		Expect(balances(dsn)).To(Equal(map[int64]int64{1: 99, 2: 20, 3: 30}))
		Expect(result.Rows).To(Equal([]query.Row{{"owner": "bob", "balance": int64(20)}}))
	})

	It("runs GO-separated batches on one session, repeating GO <count> and ignoring GO inside strings and comments", func() {
		dsn := accountsDB()
		result := run(dsn, `CREATE TEMP TABLE audit(note TEXT);
GO
INSERT INTO audit VALUES ('
GO
');
/* a block comment
GO
*/
-- GO
GO 2
UPDATE accounts SET balance = balance + $(.params.amount) WHERE id = $(.params.id)
go -- trailing comment
SELECT count(*) AS notes, (SELECT balance FROM accounts WHERE id = 2) AS balance FROM audit
`, map[string]any{"id": 2, "amount": 1})
		Expect(balances(dsn)).To(Equal(map[int64]int64{1: 10, 2: 21, 3: 30}))
		Expect(result.Rows).To(Equal([]query.Row{{"notes": int64(2), "balance": int64(21)}}))
		type batch struct{ Index, Run, StartLine, EndLine int }
		got := []batch{}
		for _, b := range result.Batches {
			got = append(got, batch{b.Index, b.Run, b.StartLine, b.EndLine})
		}
		Expect(got).To(Equal([]batch{{1, 1, 1, 1}, {2, 1, 3, 9}, {2, 2, 3, 9}, {3, 1, 11, 11}, {4, 1, 13, 13}}))
		Expect(result.Batches[3].RowsAffected).To(HaveValue(Equal(int64(1))))
	})

	It("reports each batch's end, with the rows it changed, to the context's batch observer as it runs", func() {
		dsn := accountsDB()
		var events []query.BatchEvent
		var balancesAtEnd []map[int64]int64
		ctx := query.WithBatchObserver(context.New(), func(event query.BatchEvent) {
			events = append(events, event)
			balancesAtEnd = append(balancesAtEnd, balances(dsn))
		})
		_, err := query.Execute(ctx, programProfile(dsn, "UPDATE accounts SET balance = 0 WHERE id = $(.params.id)\nGO\nDELETE FROM accounts\n"),
			map[string]any{"id": 1, "amount": 0})
		Expect(err).ToNot(HaveOccurred())
		Expect(events).To(Equal([]query.BatchEvent{
			{Batch: 1, Run: 1, StartLine: 1, Done: true, RowsAffected: new(int64(1))},
			{Batch: 2, Run: 1, StartLine: 3, Done: true, RowsAffected: new(int64(3))},
		}))
		// The first event arrives before the second batch has run.
		Expect(balancesAtEnd[0]).To(Equal(map[int64]int64{1: 0, 2: 20, 3: 30}))
	})

	It("records the program as sent, its batches bound, with each batch's arguments", func() {
		dsn := accountsDB()
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Walk: true})
		ctx := query.WithDiagnosticSink(context.New(), diagnostics)
		_, err := query.Execute(ctx, programProfile(dsn, "UPDATE accounts SET balance = $(.params.amount) WHERE id = $(.params.id)\nGO\nDELETE FROM accounts WHERE id = $(.params.id)\n"),
			map[string]any{"id": 1, "amount": 7})
		Expect(err).ToNot(HaveOccurred())
		Expect(diagnostics.Request.Query).To(Equal("UPDATE accounts SET balance = ?1 WHERE id = ?2\nGO\nDELETE FROM accounts WHERE id = ?1"))
		Expect(diagnostics.Request.Arguments).To(BeEmpty(), "each batch binds its own markers, so arguments are per batch")
		Expect(diagnostics.Request.Details).To(HaveKeyWithValue("program", HaveKeyWithValue("batches", HaveLen(2))))
	})

	It("records a single batch's arguments with its statement", func() {
		dsn := accountsDB()
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Walk: true})
		_, err := query.Execute(query.WithDiagnosticSink(context.New(), diagnostics),
			programProfile(dsn, "DELETE FROM accounts WHERE id = $(.params.id)"), map[string]any{"id": 2, "amount": 0})
		Expect(err).ToNot(HaveOccurred())
		Expect(diagnostics.Request.Query).To(Equal("DELETE FROM accounts WHERE id = ?1"))
		Expect(diagnostics.Request.Arguments).To(HaveLen(1))
	})

	It("names the failing batch and its lines and stops there", func() {
		dsn := accountsDB()
		_, err := query.Execute(context.New(), programProfile(dsn, `DELETE FROM accounts WHERE id = 1
GO

SELECT * FROM
  missing_table
GO
DELETE FROM accounts`), map[string]any{"id": 1, "amount": 0})
		Expect(err).To(MatchError(ContainSubstring("batch 2 (lines 4-5): ")))
		Expect(err).To(MatchError(ContainSubstring("missing_table")))
		Expect(balances(dsn)).To(Equal(map[int64]int64{2: 20, 3: 30}))
	})

	It("refuses a page of a program rather than ignoring it", func() {
		dsn := accountsDB()
		for _, err := range query.ExecutePages(context.New(), programProfile(dsn, "DELETE FROM accounts"), query.PageRequest{Limit: 10}) {
			Expect(err).To(MatchError(ContainSubstring("runs as a SQL program")))
			break
		}
		Expect(balances(dsn)).To(HaveLen(3))
	})

	It("keeps a SELECT on the paging path", func() {
		dsn := accountsDB()
		result := run(dsn, "SELECT owner FROM accounts WHERE id = $(.params.id)", map[string]any{"id": 1, "amount": 0})
		Expect(result.Rows).To(Equal([]query.Row{{"owner": "ann"}}))
		Expect(result.Batches).To(BeNil())
	})
})

var _ = Describe("queryFile", func() {
	It("reads a stored program from the profile's directory", func() {
		dir := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(dir, "sql"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "sql", "purge.sql"),
			[]byte("DELETE FROM accounts WHERE id = $(.params.id)\nGO\nSELECT count(*) AS left FROM accounts\n"), 0o600)).To(Succeed())
		dsn := accountsDB()
		profile := programProfile(dsn, "")
		profile.QueryFile = "sql/purge.sql"
		_, err := query.Execute(context.New(), profile, map[string]any{"id": 1, "amount": 0})
		Expect(err).To(MatchError(ContainSubstring(`queryFile "sql/purge.sql" has not been read`)))

		resolved, err := profile.ResolveQueryFile(dir)
		Expect(err).ToNot(HaveOccurred())
		result, err := query.Execute(context.New(), resolved, map[string]any{"id": 1, "amount": 0})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"left": int64(2)}}))
		Expect(resolved.Authored().Query).To(BeEmpty())
	})

	It("refuses a query and a queryFile together", func() {
		profile := programProfile("file::memory:", "SELECT 1")
		profile.QueryFile = "purge.sql"
		_, err := profile.ResolveQueryFile(GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring("sets both query and queryFile")))
		Expect(profile.Validate()).To(MatchError(ContainSubstring("sets both query and queryFile")))
	})

	It("refuses a path outside the profile's directory", func() {
		profile := programProfile("file::memory:", "")
		profile.QueryFile = "../purge.sql"
		_, err := profile.ResolveQueryFile(GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring("must be a relative path inside")))
	})
})
