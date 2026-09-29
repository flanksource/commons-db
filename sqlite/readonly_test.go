// Specs for a read-only handle: it reads what another handle writes, refuses
// every write until EnableWrites, and watches the file's data version.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("SQLite database opened read-only", func() {
	var (
		path     string
		writable *DB
		readOnly *DB
	)

	insert := func(database *DB, id string) error {
		return database.Write(func(writer *sql.DB) error {
			_, err := writer.Exec(`INSERT INTO events (id) VALUES (?)`, id)
			return err
		})
	}
	count := func(database *DB) int {
		var count int
		Expect(database.Reader().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count)).To(Succeed())
		return count
	}

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "shared.sqlite")
		var err error
		writable, err = Open(Options{Path: path})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(writable.Close()).To(Succeed()) })
		Expect(writable.Write(func(writer *sql.DB) error {
			_, err := writer.Exec(`CREATE TABLE events (id TEXT PRIMARY KEY)`)
			return err
		})).To(Succeed())
		readOnly, err = Open(Options{Path: path, ReadOnly: true, OnWriteError: func(error) {}})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(readOnly.Close()).To(Succeed()) })
	})

	It("refuses a file that does not exist rather than creating it", func() {
		missing := filepath.Join(GinkgoT().TempDir(), "missing.sqlite")
		_, err := Open(Options{Path: missing, ReadOnly: true})
		Expect(err).To(HaveOccurred())
		Expect(missing).ToNot(BeAnExistingFile())
	})

	It("reads what another handle commits", func() {
		Expect(insert(writable, "a")).To(Succeed())
		Expect(count(readOnly)).To(Equal(1))
	})

	It("refuses every write with ErrReadOnly", func() {
		Expect(errors.Is(insert(readOnly, "a"), ErrReadOnly)).To(BeTrue())
		Expect(errors.Is(readOnly.WriteAsync(func(*sql.DB) error { return nil }), ErrReadOnly)).To(BeTrue())
		Expect(errors.Is(readOnly.ExecAsync(`DELETE FROM events`), ErrReadOnly)).To(BeTrue())
		Expect(errors.Is(readOnly.InsertAsync("events", map[string]any{"id": "a"}), ErrReadOnly)).To(BeTrue())
		table := sqlitetable.Table{Name: "events", Columns: []query.ColumnDef{{Name: "id", Type: query.ColumnTypeString}}}
		Expect(errors.Is(readOnly.InsertTableAsync(table, []query.Row{{"id": "a"}}), ErrReadOnly)).To(BeTrue())
		Expect(count(writable)).To(BeZero())
	})

	It("writes once EnableWrites opens its writer", func() {
		Expect(readOnly.EnableWrites()).To(Succeed())
		Expect(insert(readOnly, "a")).To(Succeed())
		Expect(count(writable)).To(Equal(1))
		Expect(readOnly.EnableWrites()).To(Succeed(), "enabling writes twice changes nothing")
	})

	It("stops watching when its database closes, and closes only once the watch has ended", func() {
		watched := make(chan error, 1)
		go func() { watched <- readOnly.WatchDataVersion(context.Background(), 10*time.Millisecond, func() {}) }()
		Consistently(watched, 100*time.Millisecond).ShouldNot(Receive())

		Expect(readOnly.Close()).To(Succeed())
		Expect(watched).To(Receive(BeNil()), "Close returned while the watch still held a connection")
		Expect(readOnly.WatchDataVersion(context.Background(), 10*time.Millisecond, func() {})).To(Succeed(), "a closed database has nothing to watch")
	})

	It("calls back when another handle commits, until its context ends", func() {
		ctx, cancel := context.WithCancel(context.Background())
		var changes atomic.Int32
		watched := make(chan error, 1)
		go func() { watched <- readOnly.WatchDataVersion(ctx, 10*time.Millisecond, func() { changes.Add(1) }) }()
		Consistently(changes.Load, 100*time.Millisecond).Should(BeZero())

		Expect(insert(writable, "a")).To(Succeed())
		Eventually(changes.Load, time.Second).Should(BeNumerically(">=", 1))

		cancel()
		Eventually(watched, time.Second).Should(Receive(BeNil()))
	})
})
