package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"

	"ariga.io/atlas/sql/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"

	sqlitemigrate "github.com/flanksource/commons-db/migrate/sqlite"
)

// eventsTable is the table every spec starts from: keyed per stream by seq,
// with a unique index on (stream_id, name), holding one row.
const eventsTable = `CREATE TABLE "events" ("stream_id" TEXT, "seq" NUMERIC, "name" TEXT, "note" TEXT, PRIMARY KEY ("stream_id", "seq"));
	CREATE UNIQUE INDEX "events_key" ON "events" ("stream_id", "name");
	INSERT INTO "events" VALUES ('run-1', 1, 'a', 'first');`

func column(name, raw string) *schema.Column {
	return &schema.Column{Name: name, Type: &schema.ColumnType{Raw: raw, Null: true}}
}

// declaredEvents is eventsTable as declared, each column by its raw type alone.
func declaredEvents() *schema.Table {
	table := schema.NewTable("events").AddColumns(
		column("stream_id", "TEXT"), column("seq", "NUMERIC"), column("name", "TEXT"), column("note", "TEXT"),
	)
	stream, _ := table.Column("stream_id")
	seq, _ := table.Column("seq")
	name, _ := table.Column("name")
	table.SetPrimaryKey(schema.NewPrimaryKey(stream, seq))
	table.AddIndexes(schema.NewUniqueIndex("events_key").AddColumns(stream, name))
	return table
}

var _ = Describe("sqlite migrate.ReconcileTables", func() {
	var (
		ctx      context.Context
		database *sql.DB
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		database, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "migrate.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, eventsTable)
		Expect(err).ToNot(HaveOccurred())
	})

	columnsOf := func(table string) []string {
		rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		var names []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			names = append(names, name)
		}
		Expect(rows.Err()).ToNot(HaveOccurred())
		return names
	}

	schemaSQL := func() []string {
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

	It("leaves a table that matches its declaration exactly as it is", func() {
		before := schemaSQL()
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declaredEvents())).To(Succeed())
		Expect(schemaSQL()).To(Equal(before))
	})

	It("adds the columns a table lacks inside the caller's transaction, keeping its rows", func() {
		declared := declaredEvents().AddColumns(column("object_type", "TEXT"), column("count", "NUMERIC"))
		tx, err := database.BeginTx(ctx, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(sqlitemigrate.ReconcileTables(ctx, tx, sqlitemigrate.ReconcileOptions{}, declared)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		var name, note string
		var objectType, count sql.NullString
		Expect(database.QueryRowContext(ctx, `SELECT "name", "note", "object_type", "count" FROM "events" WHERE "seq" = 1`).
			Scan(&name, &note, &objectType, &count)).To(Succeed())
		Expect(map[string]any{
			"columns": columnsOf("events"), "name": name, "note": note, "objectType": objectType.Valid, "count": count.Valid,
		}).To(Equal(map[string]any{
			"columns": []string{"stream_id", "seq", "name", "note", "object_type", "count"},
			"name":    "a", "note": "first", "objectType": false, "count": false,
		}))
	})

	It("creates a declared table and index the database lacks", func() {
		audit := schema.NewTable("audit").AddColumns(column("at", "TEXT"), column("who", "TEXT"))
		who, _ := audit.Column("who")
		audit.AddIndexes(schema.NewIndex("audit_who").AddColumns(who))
		declared := declaredEvents()
		note, _ := declared.Column("note")
		declared.AddIndexes(schema.NewIndex("events_note").AddColumns(note))

		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declared, audit)).To(Succeed())
		var indexes []string
		rows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'index' AND name IN ('audit_who', 'events_note') ORDER BY name`)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		for rows.Next() {
			var index string
			Expect(rows.Scan(&index)).To(Succeed())
			indexes = append(indexes, index)
		}
		Expect(map[string]any{"audit": columnsOf("audit"), "indexes": indexes}).To(Equal(map[string]any{
			"audit": []string{"at", "who"}, "indexes": []string{"audit_who", "events_note"},
		}))
	})

	It("leaves alone the tables and views nothing declares", func() {
		_, err := database.ExecContext(ctx, `CREATE TABLE "other" ("x" TEXT); CREATE VIEW "named" AS SELECT "name" FROM "events"`)
		Expect(err).ToNot(HaveOccurred())
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declaredEvents().AddColumns(column("added", "TEXT")))).To(Succeed())
		Expect(schemaSQL()).To(ContainElements(
			`table other: CREATE TABLE "other" ("x" TEXT)`,
			`view named: CREATE VIEW "named" AS SELECT "name" FROM "events"`,
		))
	})

	// Each declaration also adds a column, to show that a refused change stops
	// the whole plan rather than applying the additions around it.
	DescribeTable("refuses, applying nothing, a change that would rewrite or discard stored rows",
		func(change func(declared *schema.Table), refused string) {
			declared := declaredEvents().AddColumns(column("added", "TEXT"))
			change(declared)
			before := schemaSQL()
			err := sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declared)
			Expect(err).To(MatchError(And(ContainSubstring(`table "events"`), ContainSubstring(refused))))
			Expect(schemaSQL()).To(Equal(before))
		},
		Entry("a dropped column", func(declared *schema.Table) {
			declared.Columns = declared.Columns[:3]
			declared.Columns = append(declared.Columns, column("added", "TEXT"))
		}, `drop column "note"`),
		Entry("a retyped column", func(declared *schema.Table) {
			note, _ := declared.Column("note")
			note.Type.Raw = "NUMERIC"
		}, `change column "note"`),
		Entry("a column made NOT NULL", func(declared *schema.Table) {
			note, _ := declared.Column("note")
			note.Type.Null = false
		}, `change column "note"`),
		Entry("a changed primary key", func(declared *schema.Table) {
			stream, _ := declared.Column("stream_id")
			declared.SetPrimaryKey(schema.NewPrimaryKey(stream))
		}, "change primary key"),
		Entry("a dropped index", func(declared *schema.Table) {
			declared.Indexes = nil
		}, `drop index "events_key"`),
		Entry("a changed index", func(declared *schema.Table) {
			stream, _ := declared.Column("stream_id")
			note, _ := declared.Column("note")
			declared.Indexes = nil
			declared.AddIndexes(schema.NewUniqueIndex("events_key").AddColumns(stream, note))
		}, `change index "events_key"`),
	)

	It("refuses to drop a unique index even with table rebuilds enabled", func() {
		declared := declaredEvents().AddColumns(column("added", "TEXT"))
		declared.Indexes = nil
		before := schemaSQL()
		err := sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{AllowRebuilds: true}, declared)
		Expect(err).To(MatchError(ContainSubstring(`drop index "events_key"`)))
		Expect(schemaSQL()).To(Equal(before))
	})

	It("refuses a declaration naming no table", func() {
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{})).To(MatchError(ContainSubstring("no table")))
	})
})
