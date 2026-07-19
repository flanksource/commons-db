package viewdeps

import (
	"context"
	"database/sql"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubQuerier satisfies Querier for argument-validation specs that must fail
// before any query is issued.
type stubQuerier struct{}

func (stubQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("stubQuerier: unexpected query")
}

const (
	reportingSchema = "reporting"
	accountsMatview = "mv_accounts_x"
)

var _ = Describe("Table", func() {
	It("qualifies against the given schema", func() {
		Expect(Table{Schema: reportingSchema, Name: "accounts"}.Qualified()).To(Equal("reporting.accounts"))
	})

	It("leaves an empty schema unqualified so search_path resolves it", func() {
		// Defaulting to "public" here would target the wrong relation whenever
		// the connection runs under a different search_path — the exact bug
		// this package exists to prevent.
		Expect(Table{Name: "accounts"}.Qualified()).To(Equal("accounts"))
	})

	It("builds refs for several names in one schema", func() {
		Expect(Tables(reportingSchema, "a", "b")).To(Equal([]Table{
			{Schema: reportingSchema, Name: "a"},
			{Schema: reportingSchema, Name: "b"},
		}))
	})

	It("builds search_path-relative refs when the schema is empty", func() {
		Expect(Tables("", "a")).To(Equal([]Table{{Name: "a"}}))
	})
})

var _ = Describe("View", func() {
	It("drops a plain view schema-qualified with CASCADE", func() {
		v := View{Schema: "public", Name: "user_report", Kind: "v"}
		Expect(v.DropStatement()).To(Equal(`DROP VIEW IF EXISTS "public"."user_report" CASCADE`))
		Expect(v.Materialized()).To(BeFalse())
	})

	It("drops a materialized view with the MATERIALIZED keyword", func() {
		v := View{Schema: reportingSchema, Name: accountsMatview, Kind: "m"}
		Expect(v.DropStatement()).To(Equal(
			`DROP MATERIALIZED VIEW IF EXISTS "reporting"."mv_accounts_x" CASCADE`))
		Expect(v.Materialized()).To(BeTrue())
	})

	It("escapes an embedded quote by doubling it", func() {
		v := View{Schema: "public", Name: `we"ird`, Kind: "v"}
		Expect(v.DropStatement()).To(Equal(`DROP VIEW IF EXISTS "public"."we""ird" CASCADE`))
	})

	It("always qualifies, so a drop never depends on search_path", func() {
		Expect(View{Schema: reportingSchema, Name: accountsMatview}.Qualified()).
			To(Equal("reporting.mv_accounts_x"))
	})
})

var _ = Describe("arrayPlaceholders", func() {
	It("numbers placeholders from one", func() {
		Expect(arrayPlaceholders(3)).To(Equal("$1,$2,$3"))
	})

	It("renders a single placeholder without a separator", func() {
		Expect(arrayPlaceholders(1)).To(Equal("$1"))
	})
})

var _ = Describe("partition", func() {
	views := []View{
		{Schema: "public", Name: "owned", Kind: "v"},
		{Schema: "public", Name: "foreign", Kind: "v"},
	}
	byName := func(want string) func(View) bool {
		return func(v View) bool { return v.Name == want }
	}

	It("splits views by the ownership predicate", func() {
		owned, unowned := partition(views, byName("owned"))
		Expect(owned).To(Equal(views[:1]))
		Expect(unowned).To(Equal(views[1:]))
	})

	It("treats every view as unowned when the predicate is nil", func() {
		owned, unowned := partition(views, nil)
		Expect(owned).To(BeEmpty())
		Expect(unowned).To(Equal(views))
	})
})

var _ = Describe("Definition.CreateStatements", func() {
	base := Definition{
		View:  View{Schema: "public", Name: "user_report", Kind: "v"},
		SQL:   " SELECT accounts.code\n   FROM accounts;",
		Owner: "xero",
	}

	It("recreates a plain view with the trailing semicolon stripped", func() {
		Expect(base.CreateStatements()).To(Equal([]string{
			"CREATE VIEW \"public\".\"user_report\" AS SELECT accounts.code\n   FROM accounts",
			`ALTER VIEW "public"."user_report" OWNER TO "xero"`,
		}))
	})

	It("creates a materialized view WITH DATA so it is queryable immediately", func() {
		mv := base
		mv.Kind = "m"
		mv.Indexes = []string{`CREATE UNIQUE INDEX pk ON public.user_report USING btree (code)`}
		Expect(mv.CreateStatements()).To(Equal([]string{
			"CREATE MATERIALIZED VIEW \"public\".\"user_report\" AS SELECT accounts.code\n   FROM accounts WITH DATA",
			`ALTER MATERIALIZED VIEW "public"."user_report" OWNER TO "xero"`,
			`CREATE UNIQUE INDEX pk ON public.user_report USING btree (code)`,
		}))
	})

	It("orders comment and grants after the indexes", func() {
		d := base
		d.Comment = "it's mine"
		d.Grants = []string{`GRANT SELECT ON "public"."user_report" TO PUBLIC`}
		Expect(d.CreateStatements()[2:]).To(Equal([]string{
			`COMMENT ON VIEW "public"."user_report" IS 'it''s mine'`,
			`GRANT SELECT ON "public"."user_report" TO PUBLIC`,
		}))
	})

	It("omits the owner statement when no owner was captured", func() {
		d := base
		d.Owner = ""
		Expect(d.CreateStatements()).To(HaveLen(1))
	})
})

var _ = Describe("grantStatement", func() {
	v := View{Schema: "public", Name: "user_report", Kind: "v"}

	It("quotes a role but leaves PUBLIC as a bare keyword", func() {
		Expect(grantStatement(v, "PUBLIC", "SELECT", false)).To(Equal(
			`GRANT SELECT ON "public"."user_report" TO PUBLIC`))
		Expect(grantStatement(v, "reader", "SELECT", false)).To(Equal(
			`GRANT SELECT ON "public"."user_report" TO "reader"`))
	})

	It("appends WITH GRANT OPTION when the privilege is grantable", func() {
		Expect(grantStatement(v, "reader", "UPDATE", true)).To(HaveSuffix(" WITH GRANT OPTION"))
	})
})

var _ = Describe("RestoreError", func() {
	It("names the view and embeds replayable DDL", func() {
		err := &RestoreError{
			View:       View{Schema: reportingSchema, Name: "user_report", Kind: "v"},
			Statements: []string{"CREATE VIEW a AS SELECT 1", "ALTER VIEW a OWNER TO x"},
			Err:        errors.New(`column "tenant_id" does not exist`),
		}
		Expect(err.Error()).To(ContainSubstring("reporting.user_report"))
		Expect(err.Error()).To(ContainSubstring(`column "tenant_id" does not exist`))
		Expect(err.Error()).To(ContainSubstring("CREATE VIEW a AS SELECT 1;\n  ALTER VIEW a OWNER TO x;"))
	})
})

var _ = Describe("fail-loud argument checks", func() {
	ctx := context.Background()
	table := Tables("", "accounts")

	It("rejects a nil Querier rather than panicking", func() {
		_, err := Dependents(ctx, nil, table)
		Expect(err).To(MatchError(ContainSubstring("Querier is nil")))
		_, err = Lookup(ctx, nil, "v")
		Expect(err).To(MatchError(ContainSubstring("Querier is nil")))
	})

	It("rejects a Sweep with no Query or no Exec", func() {
		_, _, err := Sweep(ctx, DropOptions{Tables: table, Exec: func(context.Context, string) error { return nil }})
		Expect(err).To(MatchError(ContainSubstring("Query is nil")))

		_, _, err = Sweep(ctx, DropOptions{Tables: table, Query: stubQuerier{}})
		Expect(err).To(MatchError(ContainSubstring("Exec is nil")))
	})

	It("rejects a nil Exec on Restore", func() {
		Expect(Restore(ctx, nil, []Definition{{}})).To(MatchError(ContainSubstring("Exec is nil")))
	})
})
