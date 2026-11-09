package schema

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL address form", func() {
	It("accepts URL or host and database, and rejects incomplete addresses", func() {
		for _, typ := range []string{models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse} {
			c := models.Connection{Name: "example", Type: typ, Properties: types.JSONStringMap{"host": "db.example.com", "database": "app"}}
			Expect(ValidateConnection(c)).To(Succeed(), typ)
			c.URL = typ + "://db.example.com/app"
			Expect(ValidateConnection(c)).To(Succeed(), typ)
			c.Properties = nil
			Expect(ValidateConnection(c)).To(Succeed(), typ)
			c.URL = ""
			Expect(ValidateConnection(c)).NotTo(Succeed(), typ)
			c.Properties = types.JSONStringMap{"host": "db.example.com"}
			Expect(ValidateConnection(c)).NotTo(Succeed(), typ)
		}
	})
})
