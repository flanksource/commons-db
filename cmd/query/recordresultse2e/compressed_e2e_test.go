// Specs for a result type that stores a column compressed: its profile pages
// serve the column's JSON exactly as an uncompressed column's.
package recordresultse2e

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("a record result type with a compressed column", func() {
	It("serves the column inflated in its pages", func() {
		ctx := context.Background()
		schemas := recordstore.NewSchemas()
		source := recordresultstest.NewKV(schemas)
		index, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: schemas.Kind, Derived: true, SweepInterval: time.Minute,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(index.Close)
		registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
			Prefix: "trace-results", Schemas: schemas, Index: index, Source: source, ConnectionName: "index",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
			Kind: "sample_event", Title: "Sample events", TimeColumn: "at", Compressed: []string{"detail"},
		})).To(Succeed())
		_, err = recordstore.AppendTyped(ctx, source, "run-1", "sample_event", recordresultstest.SampleEvents(1, 3))
		Expect(err).ToNot(HaveOccurred())
		server := resultServer{source: source, registry: registry, handler: serveResults(registry)}

		rows, header := server.rows("stream=run-1")
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(rows[0]["detail"]).To(Equal(map[string]any{"n": float64(3)}))
		var storedType string
		reader, err := sql.Open("sqlite", index.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		Expect(reader.QueryRowContext(ctx, `SELECT typeof(detail) FROM records_sample_event LIMIT 1`).Scan(&storedType)).To(Succeed())
		Expect(storedType).To(Equal("blob"))
	})
})
