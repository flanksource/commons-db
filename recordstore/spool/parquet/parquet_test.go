// Specs for the parquet codec: a batch spooled as parquet loads back and
// stores exactly what a direct append stores.
package parquet_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/spool/parquet"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

func TestParquet(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store Spool Parquet Suite")
}

var every = recordstore.KindSchema{Kind: "every", Columns: []query.ColumnDef{
	{Name: "name", Type: query.ColumnTypeString},
	{Name: "number", Type: query.ColumnTypeNumber},
	{Name: "ratio", Type: query.ColumnTypeNumber},
	{Name: "ok", Type: query.ColumnTypeBoolean},
	{Name: "at", Type: query.ColumnTypeDateTime},
	{Name: "took", Type: query.ColumnTypeDuration},
	{Name: "labels", Type: query.ColumnTypeKeyValue},
	{Name: "detail", Type: query.ColumnTypeJSON},
}}

func everyRow(n int) recordstore.Row {
	return recordstore.Row{
		"name": "row-" + string(rune('a'+n)), "number": int64(math.MaxInt64 - n), "ratio": 1.5 * float64(n), "ok": n%2 == 0,
		"at": time.Date(2026, 9, 29, 10, 0, n, 123456789, time.UTC), "took": time.Duration(n) * time.Second,
		"labels": map[string]string{"z": "1", "a": "2"}, "detail": map[string]any{"zeta": n, "alpha": []int{1, 2}},
	}
}

var _ = Describe("parquet codec", func() {
	ctx := context.Background()

	It("registers itself as the parquet format", func() {
		Expect(spool.Formats()).To(ContainElement(parquet.Format))
	})

	It("stores spooled rows exactly as a direct append stores them", func() {
		schemas := func(string) (recordstore.KindSchema, error) { return every, nil }
		open := func(name string) *sqlite.Backend {
			backend, err := sqlite.Open(sqlite.Options{Path: filepath.Join(GinkgoT().TempDir(), name), Schema: schemas, TTL: time.Hour, SweepInterval: time.Hour})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(backend.Close)
			return backend
		}
		rows := []recordstore.Row{everyRow(1), everyRow(2), {"name": "sparse", "detail": "just a string"}}
		direct := open("direct.sqlite")
		_, err := direct.Append(ctx, "run-1", every.Kind, rows)
		Expect(err).ToNot(HaveOccurred())

		dir, err := spool.OpenDir(filepath.Join(GinkgoT().TempDir(), "spool"))
		Expect(err).ToNot(HaveOccurred())
		_, err = dir.Publish(recordstore.Batch{
			ID: "b-1", Producer: recordstore.Producer{Instance: "cli-1", Seq: 1}, Schemas: []recordstore.KindSchema{every},
			Entries: []recordstore.BatchEntry{{Op: recordstore.BatchAppend, Stream: "run-1", Kind: every.Kind, Rows: rows}},
		}, parquet.Format)
		Expect(err).ToNot(HaveOccurred())
		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		loaded, err := dir.Load(names[0])
		Expect(err).ToNot(HaveOccurred())
		spooled := open("spooled.sqlite")
		result, err := spooled.AppendBatch(ctx, loaded)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries[0].Error).To(BeNil())

		Expect(rawRows(spooled)).To(Equal(rawRows(direct)))
	})

	It("hands numbers back as json.Number, whole int64s included, and leaves out the columns a row has no value for", func() {
		var encoded bytes.Buffer
		Expect(parquet.Codec{}.Encode(&encoded, []recordstore.Row{
			{"id": int64(math.MaxInt64), "ratio": 0.25, "name": "a"},
			{"id": int64(-1), "name": nil},
		})).To(Succeed())

		var decoded []recordstore.Row
		Expect(parquet.Codec{}.Decode(bytes.NewReader(encoded.Bytes()), func(row recordstore.Row) error {
			decoded = append(decoded, row)
			return nil
		})).To(Succeed())
		Expect(decoded).To(Equal([]recordstore.Row{
			{"id": json.Number("9223372036854775807"), "ratio": json.Number("0.25"), "name": "a"},
			{"id": json.Number("-1")},
		}))
	})

	It("widens a column of whole and fractional numbers to fractions", func() {
		var encoded bytes.Buffer
		Expect(parquet.Codec{}.Encode(&encoded, []recordstore.Row{{"n": int64(2)}, {"n": 2.5}})).To(Succeed())
		var decoded []recordstore.Row
		Expect(parquet.Codec{}.Decode(bytes.NewReader(encoded.Bytes()), func(row recordstore.Row) error {
			decoded = append(decoded, row)
			return nil
		})).To(Succeed())
		Expect(decoded).To(Equal([]recordstore.Row{{"n": json.Number("2")}, {"n": json.Number("2.5")}}))
	})

	It("refuses a column whose values have no one parquet type", func() {
		var encoded bytes.Buffer
		err := parquet.Codec{}.Encode(&encoded, []recordstore.Row{{"n": "one"}, {"n": int64(1)}})
		Expect(err).To(MatchError(ContainSubstring(`"n"`)))
	})

	It("refuses data that is not parquet", func() {
		err := parquet.Codec{}.Decode(bytes.NewReader([]byte(`{"name":"a"}`)), func(recordstore.Row) error { return nil })
		Expect(err).To(HaveOccurred())
	})

	It("stops at the first error its callback returns", func() {
		var encoded bytes.Buffer
		Expect(parquet.Codec{}.Encode(&encoded, []recordstore.Row{{"n": int64(1)}, {"n": int64(2)}})).To(Succeed())
		calls := 0
		err := parquet.Codec{}.Decode(bytes.NewReader(encoded.Bytes()), func(recordstore.Row) error {
			calls++
			return errors.New("stop")
		})
		Expect(err).To(MatchError("stop"))
		Expect(calls).To(Equal(1))
	})
})

// rawRows is every row of the every kind's table as the driver reads it.
func rawRows(backend *sqlite.Backend) [][]any {
	reader, err := sql.Open("sqlite", backend.ReadDSN())
	Expect(err).ToNot(HaveOccurred())
	defer func() { Expect(reader.Close()).To(Succeed()) }()
	rows, err := reader.Query(`SELECT * FROM records_every ORDER BY seq`)
	Expect(err).ToNot(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	columns, err := rows.Columns()
	Expect(err).ToNot(HaveOccurred())
	var all [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		Expect(rows.Scan(pointers...)).To(Succeed())
		all = append(all, values)
	}
	Expect(rows.Err()).ToNot(HaveOccurred())
	return all
}
