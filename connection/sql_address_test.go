package connection

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"strings"
)

var _ = Describe("SQL host and database", func() {
	DescribeTable("builds a driver address without placing credentials in the saved URL", func(typ, want string) {
		c := SQLConnection{Type: typ, Properties: types.JSONStringMap{"host": "db.example.com", "database": "app", "port": "15432"}}
		got, err := c.connectionString()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
		selected, err := c.UseDatabase("archive")
		Expect(err).NotTo(HaveOccurred())
		Expect(selected.connectionString()).To(Equal(strings.Replace(want, "app", "archive", 1)))
		Expect(c.Properties["database"]).To(Equal("app"))
	},
		Entry("Postgres", "postgres", "postgres://db.example.com:15432/app"),
		Entry("MySQL", "mysql", "tcp(db.example.com:15432)/app"),
		Entry("SQL Server", models.ConnectionTypeSQLServer, "sqlserver://db.example.com:15432?database=app"),
		Entry("ClickHouse", "clickhouse", "clickhouse://db.example.com:15432/app"),
	)
	It("uses an explicit URL and rejects invalid ports when building an address", func() {
		c := SQLConnection{Type: "postgres", URL: types.EnvVar{ValueStatic: "postgres://db.example.com/app"}, Properties: types.JSONStringMap{"host": "other.example.com", "database": "app"}}
		address, err := c.connectionString()
		Expect(err).NotTo(HaveOccurred())
		Expect(address).To(Equal("postgres://db.example.com/app"))
		c.URL = types.EnvVar{}
		c.Properties["port"] = "65536"
		_, err = c.connectionString()
		Expect(err).To(HaveOccurred())
	})
})
