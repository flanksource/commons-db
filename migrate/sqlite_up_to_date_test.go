package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sqlite "modernc.org/sqlite"

	"github.com/flanksource/commons-db/internal/dbtarget"
)

// recordingDriver wraps the modernc driver and records every statement and transaction a connection
// runs. Its connections expose only the prepare path, so database/sql routes every query through it.
type recordingDriver struct {
	mu         sync.Mutex
	statements []string
}

func (recorder *recordingDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &recordingConn{conn: conn, recorder: recorder}, nil
}

func (recorder *recordingDriver) record(statement string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.statements = append(recorder.statements, statement)
}

func (recorder *recordingDriver) recorded() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.statements...)
}

type recordingConn struct {
	conn     driver.Conn
	recorder *recordingDriver
}

func (conn *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return conn.PrepareContext(context.Background(), query)
}

func (conn *recordingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	conn.recorder.record(query)
	return conn.conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (conn *recordingConn) Close() error { return conn.conn.Close() }

func (conn *recordingConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *recordingConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	conn.recorder.record("BEGIN")
	return conn.conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

var registerRecordingDriver = sync.OnceValue(func() *recordingDriver {
	recorder := &recordingDriver{}
	sql.Register("sqlite-recording", recorder)
	return recorder
})

var _ = Describe("SQLite HCL target on an up-to-date database", func() {
	reconcileRecorded := func(ctx context.Context, dsn string, files fstest.MapFS) []string {
		tables, err := sqliteTables(files, resolveOptions(nil))
		Expect(err).ToNot(HaveOccurred())
		target, err := dbtarget.Parse(dsn)
		Expect(err).ToNot(HaveOccurred())
		recorder := registerRecordingDriver()
		database, err := sql.Open("sqlite-recording", target.DSN)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		before := len(recorder.recorded())
		Expect(reconcileSQLite(ctx, database, tables, false)).To(Succeed())
		return recorder.recorded()[before:]
	}
	transactional := func(statements []string) []string {
		var found []string
		for _, statement := range statements {
			if statement == "BEGIN" || strings.Contains(statement, "foreign_key_check") {
				found = append(found, statement)
			}
		}
		return found
	}

	It("opens no migration transaction and runs no foreign key check when nothing changed", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "current.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())

		Expect(transactional(reconcileRecorded(ctx, dsn, files))).To(BeEmpty())
	})

	It("still applies a pending change inside the checked transaction", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "pending.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  column "created_at" {`,
			"  column \"description\" {\n    type = text\n    null = true\n  }\n  column \"created_at\" {", 1))}

		Expect(transactional(reconcileRecorded(ctx, dsn, files))).To(Equal([]string{
			"BEGIN", "PRAGMA foreign_key_check", "PRAGMA foreign_key_check",
		}))
		Expect(transactional(reconcileRecorded(ctx, dsn, files))).To(BeEmpty())
	})
})
