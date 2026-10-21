// Package sqlserver is an Atlas driver for Microsoft SQL Server.
//
// It covers what declarative migrations of a shared database need: tables,
// columns, primary keys, indexes, foreign keys and checks from HCL, and stored
// procedures and functions from their T-SQL definitions, parsed and compared
// through sqlglot. Views, triggers, sequences, user-defined types,
// partitioning and computed columns are refused with an error, and versioned
// migration directories (revisions, snapshots, clean checks) are not
// supported.
//
// Pure diffing and planning live in sqlglot-go/migrate/atlas/sqlserver. This
// driver supplies inspection, connection management, locks and SQL execution.
//
// Inspection is scoped: InspectOptions.Tables names the objects to read --
// tables and routines alike, since SQL Server keeps both in one namespace per
// schema -- and Mode picks tables (InspectTables) and/or routines
// (InspectFuncs). Generated SQL runs at compatibility level 100.
package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"
	_ "github.com/microsoft/go-mssqldb"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

type (
	// Driver represents a SQL Server driver for introspecting database schemas,
	// generating diff between schema elements and applying migration changes.
	Driver struct {
		*conn
		schema.Differ
		schema.Inspector
		migrate.PlanApplier
	}

	// conn is a database connection and what it runs on.
	conn struct {
		schema.ExecQuerier
		version       string
		compatibility int
	}
)

// DriverName holds the name used for registration.
const DriverName = "sqlserver"

// errUnsupported is wrapped by every refusal of a versioned-migration feature.
var errUnsupported = errors.New("not supported by the SQL Server driver")

func init() {
	sqlclient.Register(
		DriverName,
		sqlclient.OpenerFunc(opener),
		sqlclient.RegisterDriverOpener(Open),
		sqlclient.RegisterTxOpener(OpenTx),
		sqlclient.RegisterCodec(codec, codec),
		sqlclient.RegisterFlavours("mssql"),
		sqlclient.RegisterURLParser(urlparse{}),
	)
}

type urlparse struct{}

// ParseURL implements the sqlclient.URLParser interface. The mssql flavour is
// rewritten to the sqlserver:// form go-mssqldb reads.
func (urlparse) ParseURL(u *url.URL) *sqlclient.URL {
	dsn := *u
	dsn.Scheme = DriverName
	return &sqlclient.URL{URL: u, DSN: dsn.String()}
}

func opener(ctx context.Context, u *url.URL) (*sqlclient.Client, error) {
	ur := urlparse{}.ParseURL(u)
	db, err := sql.Open(DriverName, ur.DSN)
	if err != nil {
		return nil, err
	}
	client, err := OpenClient(ctx, db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	client.URL = ur
	return client, nil
}

// OpenClient wraps an open database/sql handle, whatever DSN form it was
// opened with (URL, ADO or ODBC), in an Atlas client. Closing the client closes
// db.
func OpenClient(ctx context.Context, db *sql.DB) (*sqlclient.Client, error) {
	drv, err := open(ctx, db)
	if err != nil {
		return nil, err
	}
	return &sqlclient.Client{Name: DriverName, DB: db, Driver: drv}, nil
}

// Open opens a new SQL Server driver, reading the engine version and the
// database compatibility level.
func Open(db schema.ExecQuerier) (migrate.Driver, error) {
	return open(context.Background(), db)
}

func open(ctx context.Context, db schema.ExecQuerier) (*Driver, error) {
	c := &conn{ExecQuerier: db}
	rows, err := db.QueryContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), compatibility_level
		FROM sys.databases WHERE database_id = DB_ID()`)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: query server version: %w", err)
	}
	if err := sqlx.ScanOne(rows, &c.version, &c.compatibility); err != nil {
		return nil, fmt.Errorf("sqlserver: scan server version: %w", err)
	}
	return &Driver{
		conn:        c,
		Differ:      planner.DefaultDiff,
		Inspector:   &inspect{c},
		PlanApplier: &planApply{c},
	}, nil
}

// OpenTx opens a transaction.
func OpenTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*sqlclient.Tx, error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlclient.Tx{Tx: tx, CommitFn: tx.Commit, RollbackFn: tx.Rollback}, nil
}

// Version is the engine's ProductVersion, e.g. 16.0.4135.4.
func (d *Driver) Version() string { return d.version }

// CompatibilityLevel is the connected database's compatibility_level.
func (d *Driver) CompatibilityLevel() int { return d.compatibility }

// Snapshot implements migrate.Snapshoter and is not supported.
func (d *Driver) Snapshot(context.Context) (migrate.RestoreFunc, error) {
	return nil, fmt.Errorf("sqlserver: snapshot: %w", errUnsupported)
}

// CheckClean implements migrate.CleanChecker and is not supported.
func (d *Driver) CheckClean(context.Context, *migrate.TableIdent) error {
	return fmt.Errorf("sqlserver: clean check: %w", errUnsupported)
}

// FormatType converts schema type to its column form in the database.
func (*Driver) FormatType(t schema.Type) (string, error) { return planner.FormatType(t) }

// ParseType returns the schema.Type value represented by the given string.
func (*Driver) ParseType(s string) (schema.Type, error) { return planner.ParseType(s) }

// StmtBuilder is a helper method used to build statements with SQL Server
// formatting: bracket-quoted identifiers.
func (*Driver) StmtBuilder(opts migrate.PlanOptions) *sqlx.Builder {
	return &sqlx.Builder{QuoteOpening: '[', QuoteClosing: ']', Schema: opts.SchemaQualifier, Indent: opts.Indent}
}

// Lock implements schema.Locker with a session-owned sp_getapplock on a
// dedicated connection. A negative timeout waits forever and zero tries once.
func (d *Driver) Lock(ctx context.Context, name string, timeout time.Duration) (schema.UnlockFunc, error) {
	if name == "" || len(name) > 255 {
		return nil, fmt.Errorf("sqlserver: lock name must be 1-255 characters, got %d", len(name))
	}
	lockConn, err := sqlx.SingleConn(ctx, d.ExecQuerier)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: lock %q: %w", name, err)
	}
	status, err := applockStatus(ctx, lockConn, `EXEC @status = sp_getapplock @Resource = @p1,
		@LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = @p2`, name, lockTimeout(timeout))
	switch {
	case err != nil:
		return nil, errors.Join(fmt.Errorf("sqlserver: lock %q: %w", name, err), lockConn.Close())
	case status == -1:
		return nil, errors.Join(fmt.Errorf("sqlserver: lock %q: %w", name, schema.ErrLocked), lockConn.Close())
	case status < 0:
		return nil, errors.Join(fmt.Errorf("sqlserver: lock %q: sp_getapplock returned %d", name, status), lockConn.Close())
	}
	return func() error {
		status, err := applockStatus(context.WithoutCancel(ctx), lockConn,
			`EXEC @status = sp_releaseapplock @Resource = @p1, @LockOwner = 'Session'`, name)
		if err == nil && status != 0 {
			err = fmt.Errorf("sp_releaseapplock returned %d", status)
		}
		if err != nil {
			err = fmt.Errorf("sqlserver: unlock %q: %w", name, err)
		}
		return errors.Join(err, lockConn.Close())
	}, nil
}

func lockTimeout(timeout time.Duration) int64 {
	if timeout < 0 {
		return -1
	}
	return timeout.Milliseconds()
}

func applockStatus(ctx context.Context, conn schema.ExecQuerier, exec string, args ...any) (int, error) {
	rows, err := conn.QueryContext(ctx, "DECLARE @status int; "+exec+"; SELECT @status", args...)
	if err != nil {
		return 0, err
	}
	var status int
	return status, sqlx.ScanOne(rows, &status)
}

// queryer adapts the driver connection to mssqlcatalog.Queryer.
type queryer struct{ schema.ExecQuerier }

func (q queryer) QueryContext(ctx context.Context, query string, args ...any) (mssqlcatalog.Rows, error) {
	rows, err := q.ExecQuerier.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
