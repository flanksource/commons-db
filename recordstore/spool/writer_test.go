// Specs for the bulk Writer: it gathers writes into batches, cuts a batch at
// its row and byte caps, and hands each to its Deliver function in order.
package spool_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

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

var _ = Describe("Writer", func() {
	var (
		ctx       context.Context
		delivered []recordstore.Batch
		writer    *spool.Writer
	)

	named := func(first, last int) []recordstore.Row {
		var rows []recordstore.Row
		for n := first; n <= last; n++ {
			rows = append(rows, recordstore.Row{"name": fmt.Sprintf("row-%03d", n)})
		}
		return rows
	}
	schema := recordstore.KindSchema{Kind: everyType.Kind, Columns: everyType.Columns[:1]}
	open := func(options spool.WriterOptions) {
		options.Producer = spool.NewProducer("cli-1", "test")
		options.Schema = func(kind string) (recordstore.KindSchema, error) { return schema, nil }
		options.Deliver = func(_ context.Context, batch recordstore.Batch) error {
			delivered = append(delivered, batch)
			return nil
		}
		var err error
		writer, err = spool.NewWriter(options)
		Expect(err).ToNot(HaveOccurred())
	}
	rowsPerBatch := func() []int {
		var counts []int
		for _, batch := range delivered {
			count := 0
			for _, entry := range batch.Entries {
				count += len(entry.Rows)
			}
			counts = append(counts, count)
		}
		return counts
	}

	BeforeEach(func() {
		ctx = context.Background()
		delivered = nil
	})

	It("delivers what it gathered as one batch on Flush, with the kinds' schemas and the next producer seq", func() {
		open(spool.WriterOptions{})
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(1, 2))).To(Succeed())
		Expect(writer.Append(ctx, "run-2", schema.Kind, named(3, 3))).To(Succeed())
		Expect(writer.Seal(ctx, "run-1")).To(Succeed())
		Expect(delivered).To(BeEmpty())

		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(delivered).To(HaveLen(1))
		Expect(delivered[0].ID).ToNot(BeEmpty())
		Expect(delivered[0].Producer.Instance).To(Equal("cli-1"))
		Expect(delivered[0].Producer.Seq).To(Equal(int64(1)))
		Expect(delivered[0].Schemas).To(Equal([]recordstore.KindSchema{schema}))
		Expect(delivered[0].Entries).To(Equal([]recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: "run-1", Kind: schema.Kind, Rows: named(1, 2)},
			{Op: recordstore.BatchAppend, Stream: "run-2", Kind: schema.Kind, Rows: named(3, 3)},
			{Op: recordstore.BatchSeal, Stream: "run-1"},
		}))

		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(delivered).To(HaveLen(1), "an empty flush delivers nothing")
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(4, 4))).To(Succeed())
		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(delivered[1].Producer.Seq).To(Equal(int64(2)))
	})

	It("cuts a batch at its row cap, splitting an append across batches", func() {
		open(spool.WriterOptions{MaxRows: 3})
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(1, 2))).To(Succeed())
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(3, 7))).To(Succeed())
		Expect(rowsPerBatch()).To(Equal([]int{3, 3}))

		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(rowsPerBatch()).To(Equal([]int{3, 3, 1}))
		for _, batch := range delivered {
			Expect(batch.Schemas).To(Equal([]recordstore.KindSchema{schema}), "every batch carries the schema of the kind it appends")
		}
		var names []string
		for _, batch := range delivered {
			for _, entry := range batch.Entries {
				for _, row := range entry.Rows {
					names = append(names, row["name"].(string))
				}
			}
		}
		Expect(names).To(Equal([]string{"row-001", "row-002", "row-003", "row-004", "row-005", "row-006", "row-007"}))
	})

	It("gathers an append of no rows, which starts its stream", func() {
		open(spool.WriterOptions{})
		Expect(writer.Append(ctx, "run-1", schema.Kind, nil)).To(Succeed())
		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(delivered).To(HaveLen(1))
		Expect(delivered[0].Schemas).To(Equal([]recordstore.KindSchema{schema}))
		Expect(delivered[0].Entries).To(Equal([]recordstore.BatchEntry{{Op: recordstore.BatchAppend, Stream: "run-1", Kind: schema.Kind}}))
	})

	It("cuts a batch at its byte cap, keeping at least one row in each", func() {
		open(spool.WriterOptions{MaxBytes: 40})
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(1, 5))).To(Succeed())
		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(rowsPerBatch()).To(Equal([]int{2, 2, 1}))
	})

	It("refuses a kind its schema resolver refuses, gathering nothing", func() {
		writer, _ = spool.NewWriter(spool.WriterOptions{
			Producer: spool.NewProducer("cli-1", "test"),
			Schema: func(kind string) (recordstore.KindSchema, error) {
				return recordstore.KindSchema{}, errors.New("no such kind")
			},
			Deliver: func(_ context.Context, batch recordstore.Batch) error {
				delivered = append(delivered, batch)
				return nil
			},
		})
		Expect(writer.Append(ctx, "run-1", "missing", named(1, 1))).To(MatchError(ContainSubstring("no such kind")))
		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(delivered).To(BeEmpty())
	})

	It("keeps what it gathered when a delivery fails, so a later Flush retries it", func() {
		failing := true
		writer, _ = spool.NewWriter(spool.WriterOptions{
			Producer: spool.NewProducer("cli-1", "test"),
			Schema:   func(string) (recordstore.KindSchema, error) { return schema, nil },
			Deliver: func(_ context.Context, batch recordstore.Batch) error {
				if failing {
					return errors.New("disk full")
				}
				delivered = append(delivered, batch)
				return nil
			},
		})
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(1, 1))).To(Succeed())
		Expect(writer.Flush(ctx)).To(MatchError(ContainSubstring("disk full")))
		failing = false
		Expect(writer.Flush(ctx)).To(Succeed())
		Expect(rowsPerBatch()).To(Equal([]int{1}))
	})

	It("delivers through a spool directory with Publisher", func() {
		dir, err := spool.OpenDir(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		writer, err = spool.NewWriter(spool.WriterOptions{
			Producer: spool.NewProducer("cli-1", "test"),
			Schema:   func(string) (recordstore.KindSchema, error) { return schema, nil },
			Deliver:  dir.Publisher(spool.FormatNDJSON),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(writer.Append(ctx, "run-1", schema.Kind, named(1, 2))).To(Succeed())
		Expect(writer.Flush(ctx)).To(Succeed())

		names, err := dir.Incoming()
		Expect(err).ToNot(HaveOccurred())
		Expect(names).To(HaveLen(1))
		batch, err := dir.Load(names[0])
		Expect(err).ToNot(HaveOccurred())
		Expect(batch.Entries[0].Rows).To(Equal(named(1, 2)))
	})
})
