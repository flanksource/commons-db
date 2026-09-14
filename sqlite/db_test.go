package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

func TestDB(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQLite Database Suite")
}

func openTestDB(onWriteError func(error)) *DB {
	database, err := Open(Options{
		Path:         filepath.Join(GinkgoT().TempDir(), "test.sqlite"),
		OnWriteError: onWriteError,
	})
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	return database
}

var _ = Describe("SQLite database", func() {
	It("opens a serialized WAL writer and a query-only reader", func() {
		path := filepath.Join(GinkgoT().TempDir(), "records?#.sqlite")
		database, err := Open(Options{Path: path})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		absolute, err := filepath.Abs(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Path()).To(Equal(absolute))
		Expect(database.writer.Stats().MaxOpenConnections).To(Equal(1))
		Expect(database.Reader().Stats().MaxOpenConnections).To(Equal(10))

		var journalMode string
		Expect(database.Write(func(writer *sql.DB) error {
			if err := writer.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
				return err
			}
			_, err := writer.Exec(`CREATE TABLE events (id TEXT PRIMARY KEY, state TEXT)`)
			return err
		})).To(Succeed())
		Expect(journalMode).To(Equal("wal"))
		Expect(os.Stat(path)).Error().ToNot(HaveOccurred())

		var queryOnly int
		Expect(database.Reader().QueryRow(`PRAGMA query_only`).Scan(&queryOnly)).To(Succeed())
		Expect(queryOnly).To(Equal(1))
		_, err = database.Reader().Exec(`INSERT INTO events (id, state) VALUES ('reader', 'bad')`)
		Expect(err).To(HaveOccurred())

		external, err := sql.Open("sqlite", database.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(external.Close)
		Expect(external.QueryRow(`SELECT COUNT(*) FROM events`).Scan(new(int))).To(Succeed())
	})

	It("rejects a blank path", func() {
		_, err := Open(Options{})
		Expect(err).To(MatchError(ContainSubstring("database path is required")))
	})

	It("runs synchronous callbacks immediately and returns their errors", func() {
		database := openTestDB(nil)
		release := database.Lease()
		DeferCleanup(release)
		called := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- database.Write(func(*sql.DB) error {
				close(called)
				return nil
			})
		}()

		Consistently(called).WithTimeout(50 * time.Millisecond).ShouldNot(BeClosed())
		release()
		Eventually(called).Should(BeClosed())
		Eventually(done).Should(Receive(Succeed()))

		expected := errors.New("write failed")
		Expect(database.Write(func(*sql.DB) error { return expected })).To(MatchError(expected))
	})

	It("buffers named asynchronous writes and copies their inputs", func() {
		database := openTestDB(func(error) {})
		ctx := context.Background()
		Expect(database.Write(func(writer *sql.DB) error {
			_, err := writer.Exec(`CREATE TABLE "raw events" ("event id" TEXT PRIMARY KEY, state TEXT)`)
			return err
		})).To(Succeed())

		typed := sqlitetable.Table{
			Name: "typed_events",
			Columns: []query.ColumnDef{
				{Name: "id", Type: query.ColumnTypeString},
				{Name: "state", Type: query.ColumnTypeString},
			},
		}
		Expect(database.Write(func(writer *sql.DB) error { return typed.Create(ctx, writer) })).To(Succeed())

		started := make(chan struct{})
		values := map[string]any{"event id": "raw-1", "state": "new"}
		rows := []query.Row{{"id": "typed-1", "state": "ready"}}
		Expect(database.InsertAsync("raw events", values)).To(Succeed())
		Expect(database.ExecAsync(`UPDATE "raw events" SET state = ? WHERE "event id" = ?`, "updated", "raw-1")).To(Succeed())
		Expect(database.InsertTableAsync(typed, rows)).To(Succeed())
		Expect(database.WriteAsync(func(writer *sql.DB) error {
			defer close(started)
			_, err := writer.Exec(`INSERT INTO "raw events" ("event id", state) VALUES (?, ?)`, "raw-2", "custom")
			return err
		})).To(Succeed())
		values["event id"] = "mutated"
		rows[0]["id"] = "mutated"
		Expect(database.Write(func(writer *sql.DB) error {
			_, err := writer.Exec(`INSERT INTO "raw events" ("event id", state) VALUES (?, ?)`, "sync", "immediate")
			return err
		})).To(Succeed())

		Consistently(started).WithTimeout(50 * time.Millisecond).ShouldNot(BeClosed())
		Eventually(started).WithTimeout(time.Second).Should(BeClosed())

		var rawState, typedID string
		Expect(database.Reader().QueryRow(`SELECT state FROM "raw events" WHERE "event id" = ?`, "raw-1").Scan(&rawState)).To(Succeed())
		Expect(database.Reader().QueryRow(`SELECT "c0" FROM typed_events`).Scan(&typedID)).To(Succeed())
		Expect(rawState).To(Equal("updated"))
		Expect(typedID).To(Equal("typed-1"))
	})

	It("reports asynchronous failures outside the write lock and continues the batch", func() {
		failure := errors.New("asynchronous failure")
		hookErrors := make(chan error, 1)
		hookWrites := make(chan error, 1)
		continued := make(chan struct{})
		var database *DB
		database, err := Open(Options{
			Path: filepath.Join(GinkgoT().TempDir(), "errors.sqlite"),
			OnWriteError: func(err error) {
				hookErrors <- err
				hookWrites <- database.Write(func(*sql.DB) error { return nil })
			},
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })

		Expect(database.WriteAsync(func(*sql.DB) error { return failure })).To(Succeed())
		Expect(database.WriteAsync(func(*sql.DB) error { close(continued); return nil })).To(Succeed())

		Eventually(continued).WithTimeout(time.Second).Should(BeClosed())
		Eventually(hookErrors).Should(Receive(MatchError(ContainSubstring(failure.Error()))))
		Eventually(hookWrites).Should(Receive(Succeed()))
	})

	It("requires an error hook and rejects a full asynchronous queue", func() {
		withoutHook := openTestDB(nil)
		Expect(withoutHook.WriteAsync(func(*sql.DB) error { return nil })).To(MatchError(ErrWriteErrorHandlerRequired))

		database := openTestDB(func(error) {})
		release := database.Lease()
		DeferCleanup(release)
		Expect(database.WriteAsync(func(*sql.DB) error { return nil })).To(Succeed())
		time.Sleep(2 * asyncWriteBufferInterval)
		for range asyncWriteQueueCapacity {
			Expect(database.WriteAsync(func(*sql.DB) error { return nil })).To(Succeed())
		}
		Expect(database.WriteAsync(func(*sql.DB) error { return nil })).To(MatchError(ErrWriteQueueFull))
		release()
	})

	It("drains accepted writes before closing and rejects later writes", func() {
		path := filepath.Join(GinkgoT().TempDir(), "close.sqlite")
		hookErrors := make(chan error, 1)
		database, err := Open(Options{Path: path, OnWriteError: func(err error) { hookErrors <- err }})
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Write(func(writer *sql.DB) error {
			_, err := writer.Exec(`CREATE TABLE events (id TEXT PRIMARY KEY)`)
			return err
		})).To(Succeed())
		Expect(database.ExecAsync(`INSERT INTO events (id) VALUES (?)`, "accepted")).To(Succeed())
		asyncFailure := errors.New("close reports through hook")
		Expect(database.WriteAsync(func(*sql.DB) error { return asyncFailure })).To(Succeed())

		Expect(database.Close()).To(Succeed())
		Eventually(hookErrors).Should(Receive(MatchError(ContainSubstring(asyncFailure.Error()))))
		Expect(database.Close()).To(Succeed())
		Expect(database.Write(func(*sql.DB) error { return nil })).To(MatchError(ErrClosed))
		Expect(database.WriteAsync(func(*sql.DB) error { return nil })).To(MatchError(ErrClosed))

		reader, err := sql.Open("sqlite", path)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		var count int
		Expect(reader.QueryRow(`SELECT COUNT(*) FROM events WHERE id = ?`, "accepted").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})
})
