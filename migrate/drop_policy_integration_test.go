package migrate

import (
	"context"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dropPolicySchema = `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = text }
  column "name" { type = text }
  primary_key { columns = [column.id] }
}`

var _ = Describe("PostgreSQL drop policy", func() {
	It("preserves omitted columns and tables while applying additions", func(ctx context.Context) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "migration_drop_policy"})
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(dropPolicySchema)}}
		Expect(Apply(ctx, handle.DSN(), files)).To(Succeed())
		_, err := handle.SQL().ExecContext(ctx, `ALTER TABLE items ADD COLUMN note TEXT;
INSERT INTO items VALUES ('item-1', 'retained', 'keep this');
CREATE TABLE other (value TEXT); INSERT INTO other VALUES ('retained')`)
		Expect(err).NotTo(HaveOccurred())
		files["schema.hcl"].Data = []byte(dropPolicySchema + `
table "added" {
  schema = schema.public
  column "id" { type = text }
}`)
		for range 2 {
			Expect(Apply(ctx, handle.DSN(), files)).To(Succeed())
		}
		var note, other string
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT note FROM items WHERE id = 'item-1'`).Scan(&note)).To(Succeed())
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT value FROM other`).Scan(&other)).To(Succeed())
		Expect([]string{note, other}).To(Equal([]string{"keep this", "retained"}))
		var added string
		Expect(handle.SQL().QueryRowContext(ctx, `SELECT to_regclass('public.added')::text`).Scan(&added)).To(Succeed())
		Expect(added).To(Equal("added"))
	})
})
