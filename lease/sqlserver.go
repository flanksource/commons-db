package lease

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	sqlResourceLimit = 255
	// sqlLongestSuffix is the longest suffix a key's app locks add to its
	// resource: the last group bit.
	sqlLongestSuffix = ":group:256"
)

var errSQLReaderGateBusy = errors.New("SQL Server reader gate is busy before any locks were acquired")

// NewSQLServer uses session-owned app locks. Size the caller's lock pool for
// the maximum number of simultaneous holders and waiting writers.
func NewSQLServer(db *sql.DB, options Options) (Locker, error) {
	if db == nil {
		return nil, errors.New("SQL Server lock pool is required")
	}
	options, err := normalize(options)
	if err != nil {
		return nil, err
	}
	if _, err := sqlResource(Request{Scope: options.Scope, Key: strings.Repeat("k", maxNameLength)}); err != nil {
		return nil, err
	}
	begin := func(req Request, wait bool) Acquisition {
		name, err := sqlResource(req)
		if err != nil {
			return RefusedAcquisition{err}
		}
		acquisition := &sqlAcquisition{db: db, name: name, holder: req.Holder, mode: req.Mode, waiting: wait}
		if req.Mode == GroupMode {
			acquisition.bits = sqlGroupBits(req.Group)
		}
		return acquisition
	}
	holders := func(ctx context.Context, req Request) ([]Holder, error) { return sqlHolders(ctx, db, req) }
	return &Backend{Options: options, Begin: begin, Holders: holders}, nil
}

// sqlResource is the readable app-lock resource of a key. Its gate and group
// bits are named under "<resource>:".
func sqlResource(req Request) (string, error) {
	name := req.Resource()
	if len(name)+len(sqlLongestSuffix) > sqlResourceLimit {
		return "", fmt.Errorf("SQL Server app-lock resource %q with suffix %q exceeds %d characters", name, sqlLongestSuffix, sqlResourceLimit)
	}
	return name, nil
}

// sqlGroupBits encodes the group digest as the Shared (0) or IntentExclusive
// (1) mode of each group-bit app lock.
func sqlGroupBits(group string) string {
	var bits strings.Builder
	for _, digit := range Digest(group) {
		n, _ := strconv.ParseUint(string(digit), 16, 4)
		_, _ = fmt.Fprintf(&bits, "%04b", n)
	}
	return bits.String()
}

type sqlAcquisition struct {
	db                 *sql.DB
	conn               *sql.Conn
	name, bits, holder string
	mode               Mode
	waiting            bool
}

type sqlOwnership struct {
	conn       *sql.Conn
	name, bits string
	mode       Mode
}

func (a *sqlAcquisition) pin(ctx context.Context) error {
	conn, err := a.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin SQL Server lock connection: %w", err)
	}
	a.conn = conn
	if err := sqlStatus(ctx, conn, sqlSetHolder, []byte(a.holder)); err != nil {
		return err
	}
	if a.mode == WriteMode && a.waiting {
		return sqlStatus(ctx, conn, sqlRegisterWriter, a.name)
	}
	return nil
}

func (a *sqlAcquisition) Try(ctx context.Context) (Ownership, error) {
	if a.conn == nil {
		if err := a.pin(ctx); err != nil {
			return nil, dropSQLAttempt(err, a)
		}
	}
	var err error
	switch {
	case a.mode != WriteMode:
		err = sqlStatus(ctx, a.conn, sqlReadLock, a.name, a.bits)
	case a.waiting:
		err = sqlStatus(ctx, a.conn, sqlWaitingWriteLock, a.name)
	default:
		err = sqlStatus(ctx, a.conn, sqlTryWriteLock, a.name)
	}
	if err != nil {
		if a.mode != WriteMode && err == errSQLReaderGateBusy {
			return nil, a.release(ctx)
		}
		if err == ErrBusy && a.mode == WriteMode && a.waiting {
			return nil, err
		}
		return nil, dropSQLAttempt(err, a)
	}
	held := &sqlOwnership{conn: a.conn, name: a.name, bits: a.bits, mode: a.mode}
	a.conn = nil
	return held, nil
}

// release returns a reader whose gate was busy, and which therefore holds no
// app locks, to the pool after clearing its holder name, so the pool never
// hands out a session that carries a holder's CONTEXT_INFO. A session whose
// name cannot be cleared is discarded.
func (a *sqlAcquisition) release(ctx context.Context) error {
	if err := sqlStatus(ctx, a.conn, sqlClearHolder); err != nil {
		return dropSQLAttempt(fmt.Errorf("clear SQL Server lock session holder: %w", err), a)
	}
	err := a.conn.Close()
	a.conn = nil
	if err != nil {
		return fmt.Errorf("return SQL Server lock connection to pool: %w", err)
	}
	return ErrBusy
}

func dropSQLAttempt(cause error, a *sqlAcquisition) error {
	err := discardSQLConnection(a.conn)
	a.conn = nil
	if err == nil {
		return cause
	}
	return errors.Join(cause, err)
}

func (a *sqlAcquisition) Abandon(context.Context) error {
	return discardSQLConnection(a.conn)
}

func (h *sqlOwnership) Renew(ctx context.Context) error {
	var actual string
	err := h.conn.QueryRowContext(ctx, "SELECT APPLOCK_MODE(N'public', @p1, N'Session')", h.name).Scan(&actual)
	if err != nil {
		return fmt.Errorf("verify SQL Server app lock: %w", err)
	}
	if h.mode == WriteMode && actual != "Exclusive" || h.mode != WriteMode && actual != "Shared" {
		return ErrNotOwned
	}
	return nil
}

// Unlock releases the app locks and then ends the session, so the pool never
// hands out a session that carries a holder's CONTEXT_INFO.
func (h *sqlOwnership) Unlock(ctx context.Context) error {
	err := sqlStatus(ctx, h.conn, sqlUnlock, h.name, h.bits)
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
		err = nil
	}
	return errors.Join(err, discardSQLConnection(h.conn))
}

func sqlStatus(ctx context.Context, conn *sql.Conn, batch string, args ...any) error {
	var code int
	if err := conn.QueryRowContext(ctx, batch, args...).Scan(&code); err != nil {
		return fmt.Errorf("SQL Server app lock: %w", err)
	}
	switch code {
	case 0, 1:
		return nil
	case -1:
		return ErrBusy
	case -10:
		return errSQLReaderGateBusy
	default:
		return fmt.Errorf("SQL Server app lock returned %d", code)
	}
}

func discardSQLConnection(conn *sql.Conn) error {
	if conn == nil {
		return nil
	}
	// Closing a sql.Conn returns it to the pool. ErrBadConn ends the session.
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
