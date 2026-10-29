package sqlite

import (
	"database/sql"
	"net/url"
	"path/filepath"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("query-only SQLite connections", func() {
	It("reads rows and metric functions without creating tables or relaxing write protection", func() {
		path := filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		writer, err := sql.Open("sqlite", path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(writer.Close)
		_, err = writer.Exec("CREATE TABLE samples(value INTEGER)")
		Expect(err).NotTo(HaveOccurred())
		_, err = writer.Exec("INSERT INTO samples VALUES (10), (20), (30)")
		Expect(err).NotTo(HaveOccurred())
		dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=query_only(1)"
		client, _, release, err := (sqlProvider{connType: "sqlite"}).connect(context.New(), query.ProviderRequest{
			Query: "SELECT percentile(value, 50) FROM samples", Options: map[string]any{"url": dsn},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(release)
		DeferCleanup(client.Close)
		var median float64
		Expect(client.QueryRow("SELECT percentile(value, 50) FROM samples").Scan(&median)).To(Succeed())
		Expect(median).To(Equal(float64(20)))
		var queryOnly, tables int
		Expect(client.QueryRow("PRAGMA query_only").Scan(&queryOnly)).To(Succeed())
		Expect(queryOnly).To(Equal(1))
		Expect(client.QueryRow("SELECT count(*) FROM sqlite_temp_master").Scan(&tables)).To(Succeed())
		Expect(tables).To(BeZero())
		_, err = client.Exec("INSERT INTO samples VALUES (40)")
		Expect(err).To(MatchError(ContainSubstring("readonly")))
		_, err = client.Exec("CREATE TEMP TABLE forbidden(value INTEGER)")
		Expect(err).To(MatchError(ContainSubstring("readonly")))
	})
})
