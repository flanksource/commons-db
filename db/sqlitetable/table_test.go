package sqlitetable_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
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

	selectAll := func() []query.Row {
		rows, err := database.QueryContext(ctx, table.Select()+` ORDER BY "seq"`)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		result, err := db.ScanRows[query.Row](rows)
		Expect(err).ToNot(HaveOccurred())
		for _, row := range result {
			Expect(sqlitetable.DecodeStructured(table.Columns, row)).To(Succeed())
		}
		return result
	}

	It("stores columns positionally and reads them back under their declared names", func() {
		at := time.Date(2026, 9, 10, 6, 47, 7, 708159000, time.FixedZone("SAST", 2*3600))
		Expect(table.Create(ctx, database)).To(Succeed())
		Expect(table.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 1, "ok": true, "detail": map[string]any{"db": "oipa"}, "at": at},
		})).To(Succeed())
		// A second insert into the existing table is an append, not a rewrite.
		Expect(table.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 2, "ok": false, "detail": nil, "at": nil},
		})).To(Succeed())

		Expect(selectAll()).To(Equal([]query.Row{
			{"stream id": "run-1", "seq": int64(1), "ok": true, "detail": map[string]any{"db": "oipa"}, "at": "2026-09-10T04:47:07.708159000Z"},
			{"stream id": "run-1", "seq": int64(2), "ok": false, "detail": nil, "at": nil},
		}))
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
		Expect(table.Create(ctx, database)).To(Succeed())
		Expect(table.Insert(ctx, database, []query.Row{
			{"stream id": "run-1", "seq": 1, "detail": "hello"},
			{"stream id": "run-1", "seq": 2, "detail": true},
			{"stream id": "run-1", "seq": 3, "detail": json.Number("9007199254740993")},
		})).To(Succeed())

		rows := selectAll()
		Expect(rows[0]["detail"]).To(Equal("hello"))
		Expect(rows[1]["detail"]).To(Equal(true))
		Expect(rows[2]["detail"]).To(Equal(json.Number("9007199254740993")))
	})

	It("enforces the declared primary key", func() {
		Expect(table.Create(ctx, database)).To(Succeed())
		row := query.Row{"stream id": "run-1", "seq": 1}
		Expect(table.Insert(ctx, database, []query.Row{row})).To(Succeed())
		Expect(table.Insert(ctx, database, []query.Row{row})).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})

	It("indexes a unique column", func() {
		unique := sqlitetable.Table{
			Name:    "materialized",
			Columns: []query.ColumnDef{{Name: "row_id", Type: query.ColumnTypeNumber}},
			Unique:  []string{"row_id"},
		}
		Expect(sqlitetable.Write(ctx, database, unique, []query.Row{{"row_id": 1}})).To(Succeed())
		Expect(unique.Insert(ctx, database, []query.Row{{"row_id": 1}})).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})

	It("names the physical column a declared column is stored in", func() {
		physical, err := table.Physical("seq")
		Expect(err).ToNot(HaveOccurred())
		Expect(physical).To(Equal(`"c1"`))
		_, err = table.Physical("missing")
		Expect(err).To(MatchError(ContainSubstring(`column "missing"`)))
	})

	It("refuses a key naming a column the table does not declare", func() {
		table.PrimaryKey = []string{"missing"}
		Expect(table.Create(ctx, database)).To(MatchError(ContainSubstring(`column "missing"`)))
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
