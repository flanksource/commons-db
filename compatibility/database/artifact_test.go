package database_test

import (
	"github.com/flanksource/commons-db/migrate/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	_ engine.ApplyRequest = sqlserver.Request{}
	_ engine.Result       = sqlserver.Result{}
	_ engine.Outcome      = sqlserver.Applied
)

var _ = Describe("SQL Server artifact seam", func() {
	It("renders a reviewed engine artifact planned offline as one transactional script under the shared migration lock", func() {
		desired, err := dialect.DesiredFromHCL("sqlserver", `schema "dbo" {}
table "orders" {
  schema = schema.dbo
  column "id" { type = int }
}`)
		Expect(err).NotTo(HaveOccurred())
		artifact, err := engine.Plan(engine.PlanRequest{
			Dialect: "sqlserver",
			Scope:   engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
			Current: migrate.Schema{},
			Desired: desired,
		})
		Expect(err).NotTo(HaveOccurred())

		script, err := sqlserver.RenderScript(artifact, sqlserver.ScriptOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(script).To(ContainSubstring("commons-db/migrate/schema"))
		Expect(script).To(ContainSubstring("CREATE TABLE"))
		Expect(script).To(ContainSubstring("orders"))
	})
})
