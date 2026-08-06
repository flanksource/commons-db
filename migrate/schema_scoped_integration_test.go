package migrate

import (
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema-scoped migration isolation", Label("integration"), func() {
	It("applies one bundle independently to two schemas", func(ctx SpecContext) {
		GinkgoT().Setenv(dbtest.EnvCreate, "true")
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "schema_scoped_migrations"})
		filesystem := fstest.MapFS{
			"schema.hcl": &fstest.MapFile{Data: []byte(`
schema "public" {}
table "context_items" {
  schema = schema.public
  column "value" { type = text }
}`)},
			"seed.sql": &fstest.MapFile{Data: []byte("INSERT INTO context_items(value) VALUES (current_schema())")},
		}
		const firstSchema = "agent_namespace_one"
		const secondSchema = "agent_namespace_two"

		Expect(Apply(ctx, handle.DSN(), filesystem, WithName("runtime"), WithSchema(firstSchema))).To(Succeed())
		Expect(Apply(ctx, handle.DSN(), filesystem, WithName("runtime"), WithSchema(secondSchema))).To(Succeed())

		var secondHashBefore string
		Expect(handle.SQL().QueryRowContext(ctx, `
SELECT encode(hash, 'hex')
FROM agent_namespace_two.schema_migration_scripts
WHERE scope = $1 AND path = 'seed.sql'`, migrationScope(secondSchema, "runtime")).Scan(&secondHashBefore)).To(Succeed())

		filesystem["schema.hcl"] = &fstest.MapFile{Data: []byte(`
schema "public" {}
table "context_items" {
  schema = schema.public
  column "value" { type = text }
  column "detail" {
    type = text
    null = true
  }
}`)}
		filesystem["seed.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO context_items(value) VALUES (current_schema() || '-updated')")}
		Expect(Apply(ctx, handle.DSN(), filesystem, WithName("runtime"), WithSchema(firstSchema))).To(Succeed())

		var firstValue, secondValue string
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT value FROM agent_namespace_one.context_items ORDER BY value DESC LIMIT 1`).Scan(&firstValue)).To(Succeed())
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT value FROM agent_namespace_two.context_items`).Scan(&secondValue)).To(Succeed())
		Expect(firstValue).To(Equal(firstSchema + "-updated"))
		Expect(secondValue).To(Equal(secondSchema))

		var firstHasDetail, secondHasDetail bool
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = $1 AND table_name = 'context_items' AND column_name = 'detail'
)`, firstSchema).Scan(&firstHasDetail)).To(Succeed())
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = $1 AND table_name = 'context_items' AND column_name = 'detail'
)`, secondSchema).Scan(&secondHasDetail)).To(Succeed())
		Expect(firstHasDetail).To(BeTrue())
		Expect(secondHasDetail).To(BeFalse())

		var secondHashAfter string
		Expect(handle.SQL().QueryRowContext(ctx, `
SELECT encode(hash, 'hex')
FROM agent_namespace_two.schema_migration_scripts
WHERE scope = $1 AND path = 'seed.sql'`, migrationScope(secondSchema, "runtime")).Scan(&secondHashAfter)).To(Succeed())
		Expect(secondHashAfter).To(Equal(secondHashBefore))

		var publicTable *string
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT to_regclass('public.context_items')::text`).Scan(&publicTable)).To(Succeed())
		Expect(publicTable).To(BeNil())
	})
})
