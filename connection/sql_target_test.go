package connection

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL connection target", func() {
	const password = "s3cret"

	DescribeTable("reads host, port, user and database through the driver's parser, without the password",
		func(typ, url string, want SQLTarget) {
			c := SQLConnection{Type: typ, URL: types.EnvVar{ValueStatic: url}, Username: types.EnvVar{ValueStatic: "reporter"}, Password: types.EnvVar{ValueStatic: password}}
			got, err := c.Target()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
			Expect(got.String()).NotTo(ContainSubstring(password))
		},
		Entry("Postgres URL", models.ConnectionTypePostgres, "postgres://db.example.com:15432/app?sslmode=disable",
			SQLTarget{Host: "db.example.com", Port: "15432", User: "reporter", Database: "app"}),
		Entry("Postgres keyword DSN", models.ConnectionTypePostgres, "host=db.example.com dbname=app sslmode=disable",
			SQLTarget{Host: "db.example.com", Port: "5432", User: "reporter", Database: "app"}),
		Entry("MySQL", models.ConnectionTypeMySQL, "admin@tcp(db.example.com:3307)/app",
			SQLTarget{Host: "db.example.com", Port: "3307", User: "reporter", Database: "app"}),
		Entry("SQL Server", models.ConnectionTypeSQLServer, "sqlserver://db.example.com:1433?database=app",
			SQLTarget{Host: "db.example.com", Port: "1433", User: "reporter", Database: "app"}),
		Entry("ClickHouse", models.ConnectionTypeClickHouse, "clickhouse://db.example.com:9000/app",
			SQLTarget{Host: "db.example.com", Port: "9000", User: "reporter", Database: "app"}),
	)

	It("reads a sqlite connection's file as its database", func() {
		got, err := SQLConnection{Type: models.ConnectionTypeSQLite, URL: types.EnvVar{ValueStatic: "sqlite:///var/lib/app.db"}}.Target()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(SQLTarget{Database: "/var/lib/app.db"}))
	})

	It("builds the target from host and database properties", func() {
		c := SQLConnection{Type: models.ConnectionTypePostgres, Username: types.EnvVar{ValueStatic: "reporter"},
			Properties: types.JSONStringMap{"host": "db.example.com", "database": "app", "port": "15432"}}
		got, err := c.Target()
		Expect(err).NotTo(HaveOccurred())
		Expect(got.String()).To(Equal("reporter@db.example.com:15432/app"))
	})
})
