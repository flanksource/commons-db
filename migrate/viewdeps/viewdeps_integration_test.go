package viewdeps

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// startDB opens one database for the whole suite. Embedded postgres needs a
// SysV shared-memory segment, which a machine already running several instances
// can exhaust — hence one instance per suite rather than per spec, and hence
// COMMONS_DB_URL to skip embedded entirely. See resetSchema for per-spec
// isolation.
func startDB() (*sql.DB, string) {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "viewdeps_runner"})
	// Roles are cluster-global rather than per-database, so the suffix — not the
	// database — is what stops concurrent runs on one server from dropping each
	// other's role mid-spec.
	return handle.SQL(), "reporting_reader_" + handle.Unique()
}

// resetSchema returns the database to a bare state between specs, so one
// postgres instance can serve the whole suite.
func resetSchema(db *sql.DB, reader string) {
	GinkgoHelper()
	exec(db,
		`DROP SCHEMA IF EXISTS reporting CASCADE`,
		`DROP SCHEMA public CASCADE`,
		`CREATE SCHEMA public`,
		`DROP ROLE IF EXISTS `+pq.QuoteIdentifier(reader),
		`SET search_path TO public`,
	)
}

func exec(db *sql.DB, stmts ...string) {
	GinkgoHelper()
	for _, stmt := range stmts {
		_, err := db.ExecContext(context.Background(), stmt)
		Expect(err).ToNot(HaveOccurred(), stmt)
	}
}

// execer adapts *sql.DB to the Exec seam.
func execer(db *sql.DB) Exec {
	return func(ctx context.Context, stmt string) error {
		_, err := db.ExecContext(ctx, stmt)
		return err
	}
}

func names(views []View) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.Qualified()
	}
	return out
}

func scalar[T any](db *sql.DB, query string, args ...any) T {
	GinkgoHelper()
	var v T
	Expect(db.QueryRow(query, args...).Scan(&v)).To(Succeed())
	return v
}

const widenAccountsCode = `ALTER TABLE accounts ALTER COLUMN code TYPE varchar(64)`

var _ = Describe("Sweep", Ordered, func() {
	var (
		db     *sql.DB
		reader string
	)
	ctx := context.Background()

	BeforeAll(func() { db, reader = startDB() })

	BeforeEach(func() {
		resetSchema(db, reader)
		exec(db,
			`CREATE TABLE public.accounts (id text, code varchar(20), tenant_id text)`,
			`INSERT INTO public.accounts VALUES ('1', 'ACC-1', 't1')`,
		)
	})

	// This is the reported failure: a materialized view in a schema outside
	// search_path. A name-prefix sweep over pg_matviews finds it but then drops
	// it unqualified, which resolves to nothing and succeeds — so the ALTER
	// below fails much later with a confusing 0A000.
	Describe("a dependent view outside search_path", func() {
		BeforeEach(func() {
			exec(db,
				`CREATE SCHEMA reporting`,
				`SET search_path TO public`,
				`CREATE MATERIALIZED VIEW reporting.mv_accounts_x AS SELECT id, code FROM public.accounts`,
			)
		})

		It("is invisible to an unqualified name lookup", func() {
			// Proves the old convention-based path could not have dropped it.
			Expect(scalar[sql.NullString](db, `SELECT to_regclass('mv_accounts_x')::text`).Valid).To(BeFalse())
			Expect(scalar[int](db, `SELECT count(*) FROM pg_matviews
				WHERE matviewname LIKE 'mv_accounts_%' AND schemaname = ANY(current_schemas(false))`)).To(Equal(0))
		})

		It("blocks the widen until it is swept", func() {
			_, err := db.ExecContext(ctx, widenAccountsCode)
			Expect(err).To(MatchError(ContainSubstring("used by a view or rule")))

			dropped, restore, err := Sweep(ctx, DropOptions{
				Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(names(dropped)).To(Equal([]string{"reporting.mv_accounts_x"}))

			Expect(db.ExecContext(ctx, widenAccountsCode)).Error().ToNot(HaveOccurred())
			Expect(restore(ctx)).To(Succeed())

			Expect(scalar[int](db, `SELECT character_maximum_length FROM information_schema.columns
				WHERE table_name = 'accounts' AND column_name = 'code'`)).To(Equal(64))
			Expect(scalar[string](db, `SELECT code FROM reporting.mv_accounts_x`)).To(Equal("ACC-1"))
		})
	})

	Describe("a transitive chain", func() {
		BeforeEach(func() {
			exec(db,
				`CREATE VIEW v1 AS SELECT id, code FROM accounts`,
				`CREATE VIEW v2 AS SELECT id, code FROM v1`,
				`CREATE MATERIALIZED VIEW mv3 AS SELECT id, code FROM v2`,
			)
		})

		It("returns every dependent in dependency order", func() {
			deps, err := Dependents(ctx, db, Tables("", "accounts"))
			Expect(err).ToNot(HaveOccurred())
			Expect(names(deps)).To(Equal([]string{"public.v1", "public.v2", "public.mv3"}))
		})

		It("clears and rebuilds the whole chain around the widen", func() {
			_, restore, err := Sweep(ctx, DropOptions{
				Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(scalar[int](db, `SELECT count(*) FROM pg_class WHERE relname IN ('v1','v2','mv3')`)).To(Equal(0))

			Expect(db.ExecContext(ctx, widenAccountsCode)).Error().ToNot(HaveOccurred())
			Expect(restore(ctx)).To(Succeed())

			Expect(scalar[int](db, `SELECT count(*) FROM pg_class WHERE relname IN ('v1','v2','mv3')`)).To(Equal(3))
			Expect(scalar[string](db, `SELECT code FROM mv3`)).To(Equal("ACC-1"))
		})
	})

	Describe("ownership", func() {
		BeforeEach(func() {
			exec(db,
				`CREATE VIEW owned_view AS SELECT id, code FROM accounts`,
				`CREATE VIEW foreign_view AS SELECT id, code FROM accounts`,
			)
		})

		It("drops owned views without restoring them, leaving that to the caller", func() {
			_, restore, err := Sweep(ctx, DropOptions{
				Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
				Owned: func(v View) bool { return v.Name == "owned_view" },
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(db.ExecContext(ctx, widenAccountsCode)).Error().ToNot(HaveOccurred())
			Expect(restore(ctx)).To(Succeed())

			// The unowned view came back; the owned one is the caller's job —
			// restoring it from the captured definition would resurrect a stale
			// copy the caller's own recreate path would then decline to replace.
			Expect(scalar[int](db, `SELECT count(*) FROM pg_class WHERE relname = 'foreign_view'`)).To(Equal(1))
			Expect(scalar[int](db, `SELECT count(*) FROM pg_class WHERE relname = 'owned_view'`)).To(Equal(0))
		})
	})

	Describe("capture fidelity", func() {
		It("round-trips owner, comment, grants and the unique index a CONCURRENTLY refresh needs", func() {
			exec(db,
				`CREATE ROLE `+pq.QuoteIdentifier(reader),
				`CREATE MATERIALIZED VIEW mv_report AS SELECT id, code FROM accounts`,
				`CREATE UNIQUE INDEX mv_report_pk ON mv_report (id)`,
				`COMMENT ON MATERIALIZED VIEW mv_report IS 'operator''s report'`,
				`GRANT SELECT ON mv_report TO `+pq.QuoteIdentifier(reader),
			)

			_, restore, err := Sweep(ctx, DropOptions{
				Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(db.ExecContext(ctx, widenAccountsCode)).Error().ToNot(HaveOccurred())
			Expect(restore(ctx)).To(Succeed())

			Expect(scalar[string](db, `SELECT obj_description('mv_report'::regclass, 'pg_class')`)).
				To(Equal("operator's report"))
			Expect(scalar[int](db, `SELECT count(*) FROM pg_indexes
				WHERE tablename = 'mv_report' AND indexname = 'mv_report_pk'`)).To(Equal(1))
			Expect(scalar[bool](db, `SELECT has_table_privilege($1, 'mv_report', 'SELECT')`, reader)).To(BeTrue())
			// WITH DATA means it is populated, so CONCURRENTLY is legal immediately.
			Expect(scalar[bool](db, `SELECT relispopulated FROM pg_class WHERE relname = 'mv_report'`)).To(BeTrue())
			Expect(db.ExecContext(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY mv_report`)).Error().ToNot(HaveOccurred())
		})
	})

	Describe("a restore that cannot succeed", func() {
		It("fails loudly with the view named and its DDL replayable", func() {
			exec(db, `CREATE VIEW tenant_report AS SELECT id, tenant_id FROM accounts`)

			_, restore, err := Sweep(ctx, DropOptions{
				Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
			})
			Expect(err).ToNot(HaveOccurred())
			// The column the view read is gone, so the view cannot come back.
			exec(db, `ALTER TABLE accounts DROP COLUMN tenant_id`)

			err = restore(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("public.tenant_report"))
			Expect(err.Error()).To(ContainSubstring("CREATE VIEW"))
			Expect(err.Error()).To(ContainSubstring("Recreate it manually with"))

			var restoreErr *RestoreError
			Expect(errors.As(err, &restoreErr)).To(BeTrue())
			Expect(restoreErr.View.Name).To(Equal("tenant_report"))
		})
	})

	Describe("a table with no dependents", func() {
		It("is a no-op and stays one on a second sweep", func() {
			for range 2 {
				dropped, restore, err := Sweep(ctx, DropOptions{
					Tables: Tables("", "accounts"), Query: db, Exec: execer(db),
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(dropped).To(BeEmpty())
				Expect(restore(ctx)).To(Succeed())
			}
		})

		It("ignores a table that does not exist", func() {
			deps, err := Dependents(ctx, db, Tables("", "no_such_table"))
			Expect(err).ToNot(HaveOccurred())
			Expect(deps).To(BeEmpty())
		})
	})

	Describe("Lookup", func() {
		It("resolves a view to its real schema and skips non-views", func() {
			exec(db,
				`CREATE SCHEMA reporting`,
				`CREATE MATERIALIZED VIEW reporting.mv_hidden AS SELECT id FROM public.accounts`,
			)
			views, err := Lookup(ctx, db, "reporting.mv_hidden", "accounts", "no_such_relation")
			Expect(err).ToNot(HaveOccurred())
			Expect(names(views)).To(Equal([]string{"reporting.mv_hidden"}))
			Expect(views[0].Materialized()).To(BeTrue())
		})
	})
})
