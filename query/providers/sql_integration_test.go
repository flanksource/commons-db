package providers_test

import (
	"os"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The SQL provider is exercised against a real engine via embedded postgres.
// Gated behind COMMONS_DB_EMBEDDED_TEST=1 (matches db/embedded_test.go) because
// it downloads a postgres tarball on first run.
var _ = Describe("sql provider (embedded postgres)", Ordered, func() {
	var (
		dsn  string
		stop func() error
	)

	BeforeAll(func() {
		if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
			Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
		}

		var err error
		dsn, stop, err = db.StartEmbedded(db.EmbeddedConfig{
			DataDir:  GinkgoT().TempDir(),
			Database: "test",
		})
		Expect(err).ToNot(HaveOccurred())
	})

	AfterAll(func() {
		if stop != nil {
			_ = stop()
		}
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
