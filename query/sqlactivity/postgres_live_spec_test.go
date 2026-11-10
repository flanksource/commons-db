package sqlactivity

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// backend is one connection of the live spec, with the pid Postgres gave it.
type backend struct {
	conn *sql.Conn
	pid  int64
}

func openBackend(ctx context.Context, pool *sql.DB) backend {
	conn, err := pool.Conn(ctx)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(conn.Close)
	var pid int64
	Expect(conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)).To(Succeed())
	return backend{conn: conn, pid: pid}
}

func sampleOf(rows []Sample, pid int64) Sample {
	for _, row := range rows {
		if row.SessionID == pid {
			return row
		}
	}
	Fail(fmt.Sprintf("no sample of backend %d in %+v", pid, rows))
	return Sample{}
}

func inDatabase(rows []Sample, database string) []Sample {
	var kept []Sample
	for _, row := range rows {
		if row.Database == database {
			kept = append(kept, row)
		}
	}
	return kept
}

var _ = Describe("sql activity on a live Postgres", func() {
	It("reads a sleeping query, a lock waiter with its blocker, and the blocker idle in its transaction", func(spec SpecContext) {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "sqlactivity"})
		pool := db.SQL()
		var database string
		Expect(pool.QueryRowContext(spec, "SELECT current_database()").Scan(&database)).To(Succeed())
		_, err := pool.ExecContext(spec, "CREATE TABLE ledger (id int PRIMARY KEY, value int); INSERT INTO ledger VALUES (1, 0)")
		Expect(err).NotTo(HaveOccurred())

		holder, waiter, sleeper := openBackend(spec, pool), openBackend(spec, pool), openBackend(spec, pool)
		_, err = holder.conn.ExecContext(spec, "BEGIN; UPDATE ledger SET value = value + 1 WHERE id = 1")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _, _ = holder.conn.ExecContext(context.Background(), "ROLLBACK") })
		background, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		go func() { _, _ = waiter.conn.ExecContext(background, "UPDATE ledger SET value = value + 1 WHERE id = 1") }()
		go func() { _, _ = sleeper.conn.ExecContext(background, "SELECT pg_sleep(30)") }()
		Eventually(func() int {
			var waiting int
			Expect(pool.QueryRowContext(spec, "SELECT count(*) FROM pg_stat_activity WHERE pid IN ($1, $2) AND wait_event IN ('transactionid', 'PgSleep')", waiter.pid, sleeper.pid).Scan(&waiting)).To(Succeed())
			return waiting
		}).WithTimeout(10 * time.Second).Should(Equal(2))

		snapshot, err := Read(spec, pool, EnginePostgres, Filter{})
		Expect(err).NotTo(HaveOccurred())
		rows := inDatabase(snapshot.Rows, database)
		Expect(rows).To(HaveLen(3))
		for _, row := range rows {
			Expect(row.SQL).NotTo(ContainSubstring(pollMarker), "the poller read its own backend")
			Expect(row.Engine).To(Equal(EnginePostgres))
		}
		sleeping := sampleOf(rows, sleeper.pid)
		Expect(sleeping.State).To(Equal("active"))
		Expect(sleeping.WaitType).To(Equal("Timeout:PgSleep"))
		Expect(sleeping.Command).To(Equal("SELECT"))
		Expect(sleeping.SQL).To(Equal("SELECT pg_sleep(30)"))
		Expect(sleeping.ElapsedMs).To(BeNumerically(">", 0))
		blocked := sampleOf(rows, waiter.pid)
		Expect(blocked.Blocked).To(BeTrue())
		Expect(blocked.BlockedBy).To(Equal(holder.pid))
		Expect(blocked.WaitType).To(Equal("Lock:transactionid"))
		Expect(blocked.OpenTransactions).To(Equal(int64(1)))
		blocker := sampleOf(rows, holder.pid)
		Expect(blocker.State).To(Equal("idle in transaction"))
		Expect(blocker.Blocked).To(BeFalse())
		Expect(blocker.TransactionMs).To(BeNumerically(">", 0))

		narrowed, err := Read(spec, pool, EnginePostgres, Filter{SessionID: sleeper.pid})
		Expect(err).NotTo(HaveOccurred())
		Expect(narrowed.Rows).To(HaveLen(1))
		Expect(narrowed.Rows[0].SessionID).To(Equal(sleeper.pid))
	})
})
