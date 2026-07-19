package e2e

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	_ "github.com/lib/pq"
)

// The suite resolves a database (embedded, or external via COMMONS_DB_URL) in
// BeforeSuite. Nothing else in the suite queries it, so without this spec the
// e2e job would pay the full cost of starting Postgres while proving only that
// a port accepts TCP.
var _ = Describe("Postgres", func() {
	var db *sql.DB

	BeforeEach(func() {
		if !serviceManager.PostgresEnabled() {
			Skip("no database configured: set COMMONS_DB_URL or COMMONS_DB_EMBEDDED_TEST=1")
		}
		var err error
		db, err = sql.Open("postgres", serviceManager.PostgresURL())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	It("round-trips a row through a table it creates", func() {
		_, err := db.ExecContext(ctx, `CREATE TABLE e2e_smoke (id integer PRIMARY KEY, label text NOT NULL)`)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			Expect(db.ExecContext(ctx, `DROP TABLE e2e_smoke`)).Error().ToNot(HaveOccurred())
		})

		_, err = db.ExecContext(ctx, `INSERT INTO e2e_smoke (id, label) VALUES ($1, $2)`, 1, "alpha")
		Expect(err).ToNot(HaveOccurred())

		var label string
		Expect(db.QueryRowContext(ctx, `SELECT label FROM e2e_smoke WHERE id = $1`, 1).Scan(&label)).To(Succeed())
		Expect(label).To(Equal("alpha"))
	})

	It("reports a server version, proving the DSN reaches a real engine", func() {
		var version string
		Expect(db.QueryRowContext(ctx, `SHOW server_version`).Scan(&version)).To(Succeed())
		Expect(version).ToNot(BeEmpty())
	})
})
