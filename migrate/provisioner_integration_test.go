package migrate

import (
	"context"
	"database/sql"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("migration provisioner", Label("integration"), func() {
	It("runs instance reconciliation for every cloned database", func(ctx SpecContext) {
		GinkgoT().Setenv(dbtest.EnvCreate, "true")
		filesystem := fstest.MapFS{
			"schema.hcl": &fstest.MapFile{Data: []byte(`
schema "public" {}
table "clone_events" {
  schema = schema.public
  column "value" { type = text }
}`)},
			"always.sql": &fstest.MapFile{Data: []byte(`-- runs: always
INSERT INTO public.clone_events(value) VALUES ('reconciled')`)},
		}
		provisioner := NewProvisioner(filesystem, WithName("provisioner-integration"))

		first, firstCleanup, err := dbtest.Open(dbtest.Options{
			Name: "migrate_provisioner_first", Provisioner: provisioner,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(firstCleanup()).To(Succeed()) })
		second, secondCleanup, err := dbtest.Open(dbtest.Options{
			Name: "migrate_provisioner_second", Provisioner: provisioner,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(secondCleanup()).To(Succeed()) })

		Expect(cloneEventCount(ctx, first.SQL())).To(Equal(2))
		Expect(cloneEventCount(ctx, second.SQL())).To(Equal(2))
	})
})

func cloneEventCount(ctx context.Context, database *sql.DB) int {
	var count int
	Expect(database.QueryRowContext(ctx, "SELECT count(*) FROM public.clone_events").Scan(&count)).To(Succeed())
	return count
}
