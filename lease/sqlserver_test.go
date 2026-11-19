package lease

import (
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server app-lock protocol", func() {
	It("uses the encoded group in app-lock admission and release", func(ctx SpecContext) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(db.Close()).To(Succeed())
			Expect(mock.ExpectationsWereMet()).To(Succeed())
		})
		locker, err := NewSQLServer(db, Options{Scope: "test"})
		Expect(err).NotTo(HaveOccurred())
		const key = "country"
		resource := "commons-db:lease:" + digest("test", key)
		digest := sha256.Sum256([]byte("3:USA"))
		bits := fmt.Sprintf("%0256b", new(big.Int).SetBytes(digest[:]))
		mock.ExpectQuery(sqlReadLock).WithArgs(resource, bits).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
		mock.ExpectQuery(sqlUnlock).WithArgs(resource, bits).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
		mock.ExpectClose()
		lease, err := locker.GroupLock(key, "USA").Lock(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(lease.Unlock(ctx)).To(Succeed())
	})

	It("discards the session when a grouped acquisition returns contention", func(ctx SpecContext) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		mock.ExpectQuery(sqlReadLock).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(-1))
		mock.ExpectClose()
		locker, err := NewSQLServer(db, Options{Scope: "test"})
		Expect(err).NotTo(HaveOccurred())
		expectBusy(locker.GroupLock("country", "RSA").TryLock(ctx))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
