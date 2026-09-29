// Specifies that a replay runs on whatever connection the caller hands it, so a
// caller that switched a pinned connection's database replays there.
package xetrace

import (
	"context"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ReplayOne", func() {
	It("runs on a pinned connection the caller already pointed at a database", func() {
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(nil)))
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = pool.Close() }()
		ctx := context.Background()
		conn, err := pool.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = conn.Close() }()

		mock.ExpectQuery("SELECT OrderID FROM Orders").
			WillReturnRows(sqlmock.NewRows([]string{"OrderID"}).AddRow(7))

		result := ReplayOne(ctx, conn, Event{Name: EventSQLStatementCompleted, Statement: "SELECT OrderID FROM Orders"})

		Expect(result.Error).To(BeEmpty())
		Expect(result.RowCount).To(Equal(1))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
