// Specs for compressed columns: stored as zstd blobs, read back as the text
// they held through rs_inflate.
package sqlitetable_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("sqlitetable.Table compressed columns", func() {
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
		table, err = sqlitetable.Table{
			Name: "events",
			Columns: []query.ColumnDef{
				{Name: "id", Type: query.ColumnTypeString},
				{Name: "detail", Type: query.ColumnTypeJSON},
				{Name: "body", Type: query.ColumnTypeString},
			},
			Compressed: []string{"detail", "body"},
		}.Create(ctx, database)
		Expect(err).ToNot(HaveOccurred())
	})

	It("stores a compressed column as a blob and reads it back as its text", func() {
		body := strings.Repeat("a long repeated message ", 100)
		Expect(table.Insert(ctx, database, []query.Row{
			{"id": "a", "detail": map[string]any{"key": "value"}, "body": body},
			{"id": "b"},
		})).To(Succeed())

		var storedType string
		var stored []byte
		Expect(database.QueryRowContext(ctx, `SELECT typeof(body), body FROM events WHERE id = 'a'`).Scan(&storedType, &stored)).To(Succeed())
		Expect(storedType).To(Equal("blob"))
		Expect(len(stored)).To(BeNumerically("<", len(body)))

		rows, err := database.QueryContext(ctx, table.Select()+` ORDER BY id`)
		Expect(err).ToNot(HaveOccurred())
		read, err := db.ScanRows[query.Row](rows)
		Expect(err).ToNot(HaveOccurred())
		Expect(read[0]).To(And(HaveKeyWithValue("body", body), HaveKeyWithValue("detail", `{"key":"value"}`)))
		Expect(read[1]["body"]).To(BeNil())
	})

	It("declares a compressed column's storage as a blob", func() {
		declared, err := table.Declare()
		Expect(err).ToNot(HaveOccurred())
		Expect(declared.Columns[1].Type.Raw).To(Equal("BLOB"))
		Expect(declared.Columns[0].Type.Raw).To(Equal("TEXT"))
	})

	It("reads a value stored before its column was compressed as it was", func() {
		var inflated string
		Expect(database.QueryRowContext(ctx, `SELECT rs_inflate('plain text')`).Scan(&inflated)).To(Succeed())
		Expect(inflated).To(Equal("plain text"))
	})
})
