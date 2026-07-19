package providers_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The SQL provider is exercised against a real engine: COMMONS_DB_URL when one
// is configured, otherwise embedded postgres under COMMONS_DB_EMBEDDED_TEST=1,
// which downloads a postgres tarball on first run.
var _ = Describe("sql provider (postgres)", Ordered, func() {
	var dsn string

	BeforeAll(func() {
		dsn = dbtest.ForGinkgo(dbtest.Options{Name: "sql_provider"}).DSN()
	})

	It("executes SQL via an inline postgres URL and returns typed rows", func() {
		result, err := query.Execute(context.New(), query.Profile{
			Name: "sql-inline",
			Provider: query.ProviderConfig{
				Type:    "sql",
				Options: map[string]any{"type": "postgres", "url": dsn},
			},
			Query: "select 1 as n, 'alpha' as label",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKey("n"))
		Expect(result.Rows[0]).To(HaveKey("label"))
	})
})
