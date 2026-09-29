// Specs for a kind's compressed columns in a sqlite file: stored as zstd
// blobs, read back whole, and recorded so a changed declaration is refused.
package sqlite_test

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("sqlite backend compressed columns", func() {
	var (
		ctx   context.Context
		path  string
		clock *fakeClock
	)

	schema := func(compressed ...string) recordstore.SchemaResolver {
		return func(kind string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: kind, Columns: []query.ColumnDef{
				{Name: "name", Type: query.ColumnTypeString},
				{Name: "detail", Type: query.ColumnTypeJSON},
			}, Options: recordstore.KindOptions{Compressed: compressed}}, nil
		}
	}
	open := func(resolver recordstore.SchemaResolver) *sqlite.Backend {
		backend := openSQLite(path, clock, resolver, false)
		DeferCleanup(backend.Close)
		return backend
	}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
	})

	It("stores a compressed column as a blob, reads it back whole, and records it", func() {
		backend := open(schema("detail"))
		detail := map[string]any{"text": strings.Repeat("payload ", 200)}
		_, err := backend.Append(ctx, "run-1", "blobs", []recordstore.Row{{"name": "a", "detail": detail}})
		Expect(err).ToNot(HaveOccurred())

		var storedType string
		Expect(readOnly(backend.Path()).QueryRowContext(ctx, `SELECT typeof(detail) FROM records_blobs`).Scan(&storedType)).To(Succeed())
		Expect(storedType).To(Equal("blob"))
		_, rows := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(rows[0]["detail"]).To(Equal(map[string]any{"text": strings.Repeat("payload ", 200)}))
		Expect(kindColumns(ctx, backend.Path(), "blobs")).To(ContainSubstring(`"detail=detail:BLOB:json"`))
	})

	It("refuses to reopen a durable file whose kind no longer compresses the column", func() {
		backend := open(schema("detail"))
		_, err := backend.Append(ctx, "run-1", "blobs", []recordstore.Row{{"name": "a", "detail": map[string]any{"a": 1}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Close()).To(Succeed())

		plain := open(schema())
		_, err = plain.Append(ctx, "run-1", "blobs", []recordstore.Row{{"name": "b"}})
		Expect(err).To(MatchError(ContainSubstring(`column "detail" stored as BLOB:json, now TEXT:json`)))
	})
})
