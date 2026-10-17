package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/internal/dbtarget"
)

var _ = Describe("Apply WithDB", func() {
	openSQLiteHandle := func(name string) *sql.DB {
		target, err := dbtarget.Parse(filepath.Join(GinkgoT().TempDir(), name))
		Expect(err).ToNot(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		return database
	}
	files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}

	It("migrates a caller's SQLite handle and leaves it open with its pool settings", func(ctx context.Context) {
		database := openSQLiteHandle("handle.db")
		database.SetMaxOpenConns(4)

		Expect(Apply(ctx, "", files, WithDB(database))).To(Succeed())

		var tables int
		Expect(database.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name IN ('projects', 'events')`).Scan(&tables)).To(Succeed())
		Expect(tables).To(Equal(2))
		Expect(database.Stats().MaxOpenConnections).To(Equal(4))
		Expect(Apply(ctx, "", files, WithDB(database))).To(Succeed())
	})

	It("refuses a connection string alongside a handle", func(ctx context.Context) {
		database := openSQLiteHandle("both.db")
		Expect(Apply(ctx, "state/other.db", files, WithDB(database))).
			To(MatchError("pass a connection string or WithDB, not both"))
	})

	It("refuses an empty connection string without a handle", func(ctx context.Context) {
		Expect(Apply(ctx, " ", files)).To(MatchError("connection string is empty"))
	})

	It("refuses a PostgreSQL handle", func(ctx context.Context) {
		database, err := sql.Open("postgres", "postgres://localhost/acme?sslmode=disable")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		Expect(Apply(ctx, "", files, WithDB(database))).
			To(MatchError(ContainSubstring("WithDB does not support PostgreSQL")))
	})
})
