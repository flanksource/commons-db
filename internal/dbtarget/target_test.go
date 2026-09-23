package dbtarget_test

import (
	"net/url"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/internal/dbtarget"
)

var _ = Describe("database target DSNs", func() {
	DescribeTable("classifies PostgreSQL without rewriting its DSN",
		func(dsn string) {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			Expect(target).To(Equal(dbtarget.Target{Dialect: dbtarget.Postgres, DSN: dsn}))
		},
		Entry("postgres URL", "postgres://localhost/app?sslmode=disable"),
		Entry("postgresql URL", "postgresql://localhost/app?sslmode=disable"),
		Entry("keyword DSN whose database ends in db", "host=localhost dbname=example.db sslmode=disable"),
	)

	DescribeTable("classifies and normalizes SQLite",
		func(dsn, expectedPath string) {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			Expect(target.Dialect).To(Equal(dbtarget.SQLite))

			parsed, err := url.Parse(target.DSN)
			Expect(err).ToNot(HaveOccurred())
			Expect(parsed.Scheme).To(Equal("file"))
			Expect(parsed.Path).To(Equal(expectedPath))
			Expect(parsed.Query()["_pragma"]).To(ConsistOf("foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"))
		},
		Entry("absolute sqlite URL", "sqlite:///var/data/uir.db", "/var/data/uir.db"),
		Entry("explicit relative sqlite URL with any extension", "sqlite://state/uir.sqlite", absolutePath("state/uir.sqlite")),
		Entry("bare relative db path", "state/uir.db", absolutePath("state/uir.db")),
	)

	It("preserves SQLite query parameters and replaces managed pragmas", func() {
		target, err := dbtarget.Parse("sqlite://state/uir.db?cache=shared&_pragma=foreign_keys(0)&_pragma=case_sensitive_like(1)")
		Expect(err).ToNot(HaveOccurred())
		parsed, err := url.Parse(target.DSN)
		Expect(err).ToNot(HaveOccurred())
		Expect(parsed.Query().Get("cache")).To(Equal("shared"))
		Expect(parsed.Query()["_pragma"]).To(ConsistOf(
			"case_sensitive_like(1)", "foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)",
		))
	})

	DescribeTable("rejects invalid or unsupported DSNs",
		func(dsn, message string) {
			_, err := dbtarget.Parse(dsn)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty", " ", "DSN is required"),
		Entry("unknown URL scheme", "mysql://localhost/app", `unsupported database scheme "mysql"`),
		Entry("explicit memory database", "sqlite://:memory:", "file-backed"),
		Entry("file URI memory database", "sqlite://file::memory:?cache=shared", "file-backed"),
		Entry("bare memory database", ":memory:", "sqlite://"),
	)
})

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return filepath.Clean(strings.TrimSpace(absolute))
}
