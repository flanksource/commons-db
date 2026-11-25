package lease_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/leasetest"
	_ "github.com/microsoft/go-mssqldb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Lease Suite")
}

var _ = Describe("lease backends", func() {
	files := map[string]string{}
	leasetest.LockerContract("File", leasetest.Capabilities{}, func(options lease.ExpiringOptions) (lease.Locker, error) {
		if files[options.Scope] == "" {
			files[options.Scope] = GinkgoT().TempDir()
		}
		return lease.NewFile(files[options.Scope], options.Options)
	})
})

var _ = Describe("live lease backends", Label("integration"), func() {
	newSQLServer := func(options lease.ExpiringOptions) (lease.Locker, error) {
		db, err := sql.Open("sqlserver", leasetest.TestSetting("COMMONS_DB_LEASE_SQLSERVER_URL"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		return lease.NewSQLServer(db, options.Options)
	}

	leasetest.LockerContract("SQL Server", leasetest.AllModes, newSQLServer)
	leasetest.HoldersContract("SQL Server", leasetest.HolderBackend{
		Factory: newSQLServer,
		Lose: func(_ context.Context, held lease.Ownership) {
			Expect(lease.DiscardSQLOwnership(held)).To(Succeed())
		},
	})
})
