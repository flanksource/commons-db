package sqlitetable_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"ariga.io/atlas/sql/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
	sqlitemigrate "github.com/flanksource/commons-db/migrate/sqlite"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("sqlitetable.Table", func() {
	var (
		ctx      context.Context
		database *sql.DB
		table    sqlitetable.Table
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		database, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "table.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		table = sqlitetable.Table{
			Name: "events",
			Columns: []query.ColumnDef{
				{Name: "stream id", Type: query.ColumnTypeString},
				{Name: "seq", Type: query.ColumnTypeNumber},
				{Name: "ok", Type: query.ColumnTypeBoolean},
				{Name: "detail", Type: query.ColumnTypeJSON},
				{Name: "at", Type: query.ColumnTypeDateTime},
			},
			PrimaryKey: []string{"stream id", "seq"},
		}
	})

	selectAll := func(created sqlitetable.Table) []query.Row {
		rows, err := database.QueryContext(ctx, created.Select()+` ORDER BY "seq"`)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		result, err := db.ScanRows[query.Row](rows)
		Expect(err).ToNot(HaveOccurred())
		for _, row := range result {
			Expect(sqlitetable.DecodeStructured(created.Columns, row)).To(Succeed())
		}
		return result
	}

	physicalColumns := func(name string) []string {
		rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, name)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		var names []string
		for rows.Next() {
			var column string
			Expect(rows.Scan(&column)).To(Succeed())
			names = append(names, column)
		}
		Expect(rows.Err()).ToNot(HaveOccurred())
		return names
	}

	schemaObjects := func() []string {
		rows, err := database.QueryContext(ctx, `SELECT type || ' ' || name || ': ' || coalesce(sql, '') FROM sqlite_schema ORDER BY type, name`)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		var objects []string
		for rows.Next() {
			var object string
			Expect(rows.Scan(&object)).To(Succeed())
			objects = append(objects, object)
		}
		Expect(rows.Err()).ToNot(HaveOccurred())
		return objects
	}

	It("stores columns under derived safe names and reads them back under their declared names", func() {
		at := time.Date(2026, 9, 10, 6, 47, 7, 708159000, time.FixedZone("SAST", 2*3600))
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.StoredAs).To(Equal([]string{"stream_id", "seq", "ok", "detail", "at"}))
		Expect(physicalColumns("events")).To(Equal([]string{"stream_id", "seq", "ok", "detail", "at"}))
		Expect(created.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 1, "ok": true, "detail": map[string]any{"db": "oipa"}, "at": at},
		})).To(Succeed())
		// A second insert into the existing table is an append, not a rewrite.
		Expect(created.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 2, "ok": false, "detail": nil, "at": nil},
		})).To(Succeed())

		Expect(selectAll(created)).To(Equal([]query.Row{
			{"stream id": "run-1", "seq": int64(1), "ok": true, "detail": map[string]any{"db": "oipa"}, "at": "2026-09-10T04:47:07.708159000Z"},
			{"stream id": "run-1", "seq": int64(2), "ok": false, "detail": nil, "at": nil},
		}))
	})

	It("aliases only the columns whose physical name differs", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.Select()).To(Equal(`SELECT "stream_id" AS "stream id", "seq", "ok", "detail", "at" FROM "events"`))
	})

	It("keeps a reserved column's own name and renumbers the declared column clashing with it", func() {
		reserved := sqlitetable.Table{
			Name: "records",
			Columns: []query.ColumnDef{
				{Name: "stream_id", Type: query.ColumnTypeString},
				{Name: "Stream ID", Type: query.ColumnTypeString},
				{Name: "transaction", Type: query.ColumnTypeString},
			},
			Reserved: []string{"stream_id"},
		}
		created, err := reserved.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.StoredAs).To(Equal([]string{"stream_id", "Stream_ID_2", "transaction_"}))
	})

	It("refuses a reserved name that is not itself a safe bare name", func() {
		table.Reserved = []string{"stream id"}
		_, err := table.Create(ctx, database)
		Expect(err).To(MatchError(ContainSubstring(`reserved column "stream id"`)))
	})

	// The names a table was created with are persisted by its owner and handed
	// back; a later build deriving differently must still read the table.
	It("reads a table through the physical names it was created with, not a fresh derivation", func() {
		table.StoredAs = []string{"sid", "n", "flag", "body", "when_at"}
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(physicalColumns("events")).To(Equal([]string{"sid", "n", "flag", "body", "when_at"}))
		Expect(created.Insert(ctx, database, []query.Row{{"stream id": "run-1", "seq": 1, "ok": true}})).To(Succeed())

		reopened := sqlitetable.Table{Name: "events", Columns: table.Columns, StoredAs: []string{"sid", "n", "flag", "body", "when_at"}}
		Expect(selectAll(reopened)).To(Equal([]query.Row{
			{"stream id": "run-1", "seq": int64(1), "ok": true, "detail": nil, "at": nil},
		}))
	})

	// A column declared "c2" derives the name another column is positionally
	// stored in, so renaming one column at a time would collide.
	It("renames a positional table's columns to its derived names, keeping its rows and keys", func() {
		_, err := database.ExecContext(ctx, `CREATE TABLE "positional" ("c0" TEXT, "c1" TEXT, "c2" NUMERIC, PRIMARY KEY ("c0", "c2"));
			INSERT INTO "positional" VALUES ('run-1', 'a', 1)`)
		Expect(err).ToNot(HaveOccurred())
		positional, err := sqlitetable.Table{Name: "positional", Columns: []query.ColumnDef{
			{Name: "stream id", Type: query.ColumnTypeString},
			{Name: "c2", Type: query.ColumnTypeString},
			{Name: "seq", Type: query.ColumnTypeNumber},
		}}.Derive(ctx, database)
		Expect(err).ToNot(HaveOccurred())

		Expect(positional.RenamePositional(ctx, database)).To(Succeed())
		Expect(sqlitetable.PhysicalColumns(ctx, database, "positional")).To(Equal([]string{"stream_id", "c2", "seq"}))
		Expect(selectAll(positional)).To(Equal([]query.Row{{"stream id": "run-1", "c2": "a", "seq": int64(1)}}))
		Expect(positional.Insert(ctx, database, []query.Row{{"stream id": "run-1", "c2": "b", "seq": 1}})).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})

	It("refuses to rename a table that is not positional", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.RenamePositional(ctx, database)).To(MatchError(ContainSubstring(`not positional`)))
	})

	It("refuses stored names that do not line up with the columns", func() {
		table.StoredAs = []string{"sid"}
		_, err := table.Create(ctx, database)
		Expect(err).To(MatchError(ContainSubstring("1 stored names for 5 columns")))
		_, err = table.Physical("seq")
		Expect(err).To(MatchError(ContainSubstring("1 stored names for 5 columns")))
	})

	DescribeTable("formats every instant to one width in UTC, so text order is time order",
		func(at time.Time, expected string) {
			Expect(sqlitetable.FormatTime(at)).To(Equal(expected))
			Expect(sqlitetable.Value(query.ColumnTypeDateTime, at)).To(Equal(expected))
		},
		Entry("a whole second", time.Date(2026, 9, 10, 6, 0, 5, 0, time.UTC), "2026-09-10T06:00:05.000000000Z"),
		Entry("a fraction", time.Date(2026, 9, 10, 6, 0, 5, 500_000_000, time.UTC), "2026-09-10T06:00:05.500000000Z"),
		Entry("another zone", time.Date(2026, 9, 10, 8, 0, 5, 1, time.FixedZone("SAST", 2*3600)), "2026-09-10T06:00:05.000000001Z"),
	)

	It("round-trips every JSON scalar without changing its type or integer precision", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 1, "detail": "hello"},
			{"stream id": "run-1", "seq": 2, "detail": true},
			{"stream id": "run-1", "seq": 3, "detail": json.Number("9007199254740993")},
		})).To(Succeed())

		rows := selectAll(created)
		Expect(rows[0]["detail"]).To(Equal("hello"))
		Expect(rows[1]["detail"]).To(Equal(true))
		Expect(rows[2]["detail"]).To(Equal(json.Number("9007199254740993")))
	})

	It("enforces the declared primary key", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		row := query.Row{"stream id": "run-1", "seq": 1}
		Expect(created.Insert(ctx, database, []query.Row{row})).To(Succeed())
		Expect(created.Insert(ctx, database, []query.Row{row})).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})

	It("indexes a unique column", func() {
		unique := sqlitetable.Table{
			Name:    "materialized",
			Columns: []query.ColumnDef{{Name: "row_id", Type: query.ColumnTypeNumber}},
			Unique:  []string{"row_id"},
		}
		written, err := sqlitetable.Write(ctx, database, unique, []query.Row{{"row_id": 1}})
		Expect(err).ToNot(HaveOccurred())
		Expect(written.Insert(ctx, database, []query.Row{{"row_id": 1}})).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})

	// Another build sharing the file may declare a column this one does not.
	It("inserts into a table carrying a column it does not declare, leaving that column NULL", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.ExecContext(ctx, `ALTER TABLE "events" ADD COLUMN "declared_elsewhere" TEXT`)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.Insert(ctx, database, []query.Row{{"stream id": "run-1", "seq": 1, "ok": true}})).To(Succeed())

		var elsewhere sql.NullString
		Expect(database.QueryRowContext(ctx, `SELECT "declared_elsewhere" FROM "events"`).Scan(&elsewhere)).To(Succeed())
		Expect(map[string]any{"rows": selectAll(created), "elsewhere": elsewhere.Valid}).To(Equal(map[string]any{
			"rows":      []query.Row{{"stream id": "run-1", "seq": int64(1), "ok": true, "detail": nil, "at": nil}},
			"elsewhere": false,
		}))
	})

	It("declares for Atlas exactly the table Create creates", func() {
		table.Unique = []string{"at"}
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		declared, err := created.Declare()
		Expect(err).ToNot(HaveOccurred())
		before := schemaObjects()
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declared)).To(Succeed())
		Expect(schemaObjects()).To(Equal(before))

		declared, err = created.Declare()
		Expect(err).ToNot(HaveOccurred())
		declared.AddColumns(&schema.Column{Name: "added", Type: &schema.ColumnType{Raw: sqlitetable.Type(query.ColumnTypeNumber), Null: true}})
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declared)).To(Succeed())
		Expect(physicalColumns("events")).To(Equal([]string{"stream_id", "seq", "ok", "detail", "at", "added"}))
	})

	It("refuses to declare a table whose stored names do not line up with its columns", func() {
		table.StoredAs = []string{"sid"}
		_, err := table.Declare()
		Expect(err).To(MatchError(ContainSubstring("1 stored names for 5 columns")))
	})

	It("names the quoted physical column a declared column is stored in", func() {
		created, err := table.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
		physical, err := created.Physical("stream id")
		Expect(err).ToNot(HaveOccurred())
		Expect(physical).To(Equal(`"stream_id"`))
		_, err = created.Physical("missing")
		Expect(err).To(MatchError(ContainSubstring(`column "missing"`)))
	})

	It("refuses a key naming a column the table does not declare", func() {
		table.PrimaryKey = []string{"missing"}
		_, err := table.Create(ctx, database)
		Expect(err).To(MatchError(ContainSubstring(`column "missing"`)))
	})

	It("reads structured columns through their JSON text in a profile", func() {
		Expect(sqlitetable.ProfileColumns(table.Columns)).To(Equal([]query.ColumnDef{
			{Name: "stream id", Type: query.ColumnTypeString},
			{Name: "seq", Type: query.ColumnTypeNumber},
			{Name: "ok", Type: query.ColumnTypeBoolean},
			{Name: "detail", Type: query.ColumnTypeJSON, Source: "detail", JSONPath: "$"},
			{Name: "at", Type: query.ColumnTypeDateTime},
		}))
	})
})
