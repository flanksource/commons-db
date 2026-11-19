package lease

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server app-lock protocol", func() {
	newMock := func() (Locker, sqlmock.Sqlmock) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(db.Close()).To(Succeed())
			Expect(mock.ExpectationsWereMet()).To(Succeed())
		})
		locker, err := NewSQLServer(db, Options{Scope: "test"})
		Expect(err).NotTo(HaveOccurred())
		return locker, mock
	}
	status := func(code int) *sqlmock.Rows { return sqlmock.NewRows([]string{"status"}).AddRow(code) }

	It("names the holder, then takes and releases the readable resource with the encoded group", func(ctx SpecContext) {
		locker, mock := newMock()
		digest := sha256.Sum256([]byte("3:USA"))
		bits := fmt.Sprintf("%0256b", new(big.Int).SetBytes(digest[:]))
		mock.ExpectQuery(sqlSetHolder).WithArgs([]byte("ci-runner")).WillReturnRows(status(0))
		mock.ExpectQuery(sqlReadLock).WithArgs("test:country", bits).WillReturnRows(status(0))
		mock.ExpectQuery(sqlUnlock).WithArgs("test:country", bits).WillReturnRows(status(0))
		mock.ExpectClose()
		lease, err := locker.GroupLock("country", "USA", as("ci-runner")).Lock(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(lease.Unlock(ctx)).To(Succeed())
	})

	It("discards the session when a grouped acquisition returns contention", func(ctx SpecContext) {
		locker, mock := newMock()
		mock.ExpectQuery(sqlSetHolder).WithArgs(sqlmock.AnyArg()).WillReturnRows(status(0))
		mock.ExpectQuery(sqlReadLock).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(status(-1))
		mock.ExpectClose()
		expectBusy(locker.GroupLock("country", "RSA", as("ci-runner")).TryLock(ctx))
	})

	It("names the missing grant instead of reporting no holders", func(ctx SpecContext) {
		locker, mock := newMock()
		mock.ExpectQuery(sqlHolderPermission).WillReturnRows(sqlmock.NewRows([]string{"granted", "login"}).AddRow(0, "lease_app"))
		mock.ExpectClose()
		_, err := locker.Mutex("deploy", as("probe")).Holders(ctx)
		Expect(err).To(MatchError(ContainSubstring("need the VIEW SERVER STATE permission to read sys.dm_tran_locks: GRANT VIEW SERVER STATE TO [lease_app]")))
	})

	It("classifies sessions by the truncated descriptions of the key's own app locks", func(ctx SpecContext) {
		locker, mock := newMock()
		key := "deployment-pipeline-key"
		name := "test:" + key
		Expect(len(name)).To(BeNumerically("<", sqlDescriptionChars))
		describe := func(resource string) string {
			return "0:[" + truncate(resource, sqlDescriptionChars) + "]:(a1b2c3d4)"
		}
		login := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
		rows := sqlmock.NewRows([]string{"session", "mode", "description", "context_info", "login"}).
			AddRow(51, "X", describe(name), padded("writer"), login).
			AddRow(52, "S", describe(name), padded("reader"), login).
			AddRow(53, "S", describe(name+"-other"), padded("other-key"), login).
			AddRow(54, "IX", describe(name+":gate"), padded("waiting"), login).
			AddRow(55, "S", describe(name), padded("grouped"), login)
		for i := 1; i <= 256; i++ {
			rows.AddRow(55, "IX", describe(name+":group:"+strconv.Itoa(i)), padded("grouped"), login)
		}
		mock.ExpectQuery(sqlHolderPermission).WillReturnRows(sqlmock.NewRows([]string{"granted", "login"}).AddRow(1, "lease_app"))
		mock.ExpectQuery(sqlHolderLocks).WithArgs("[" + name).WillReturnRows(rows)
		mock.ExpectClose()
		holders, err := locker.Mutex(key, as("probe")).Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(
			Holder{Name: "writer", Mode: "write", Since: login},
			Holder{Name: "reader", Mode: "read", Since: login},
			Holder{Name: "grouped", Mode: "group", Since: login},
		))
	})
})

// padded mimics a CONTEXT_INFO value read back with trailing zero bytes.
func padded(holder string) []byte {
	return []byte(holder + strings.Repeat("\x00", maxHolderLength-len(holder)))
}
