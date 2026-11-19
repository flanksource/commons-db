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
	return &locker{options: options, begin: func(req request, wait bool) acquisition {
		acquisition := &sqlAcquisition{db: db, name: "commons-db:lease:" + req.key, mode: req.mode, waiting: wait}
		if req.mode == groupMode {
			var bits strings.Builder
			for _, digit := range req.group {
				n, _ := strconv.ParseUint(string(digit), 16, 4)
				_, _ = fmt.Fprintf(&bits, "%04b", n)
			}
			acquisition.bits = bits.String()
		}
		return acquisition
	}}, nil
}

type sqlAcquisition struct {
	db         *sql.DB
	conn       *sql.Conn
	name, bits string
	mode       mode
	waiting    bool
}

type sqlOwnership struct {
	conn       *sql.Conn
	name, bits string
	mode       mode
}

func (a *sqlAcquisition) try(ctx context.Context) (ownership, error) {
	if a.conn == nil {
		conn, err := a.db.Conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("pin SQL Server lock connection: %w", err)
		}
		a.conn = conn
		if a.mode == writeMode && a.waiting {
			if err := sqlStatus(ctx, conn, sqlRegisterWriter, a.name); err != nil {
				return nil, dropSQLAttempt(err, a)
			}
		}
	}
	var err error
	switch {
	case a.mode != writeMode:
		err = sqlStatus(ctx, a.conn, sqlReadLock, a.name, a.bits)
	case a.waiting:
		err = sqlStatus(ctx, a.conn, sqlWaitingWriteLock, a.name)
	default:
		err = sqlStatus(ctx, a.conn, sqlTryWriteLock, a.name)
	}
	if err != nil {
		if err == errBusy && a.mode == writeMode && a.waiting {
			return nil, err
		}
		return nil, dropSQLAttempt(err, a)
	}
	held := &sqlOwnership{conn: a.conn, name: a.name, bits: a.bits, mode: a.mode}
	a.conn = nil
	return held, nil
}

func dropSQLAttempt(cause error, a *sqlAcquisition) error {
	err := discardSQLConnection(a.conn)
	a.conn = nil
	if err == nil {
		return cause
	}
	return errors.Join(cause, err)
}

func (a *sqlAcquisition) abandon(context.Context) error {
	return discardSQLConnection(a.conn)
}

func (h *sqlOwnership) renew(ctx context.Context) error {
	var actual string
	err := h.conn.QueryRowContext(ctx, "SELECT APPLOCK_MODE(N'public', @p1, N'Session')", h.name).Scan(&actual)
	if err != nil {
		return fmt.Errorf("verify SQL Server app lock: %w", err)
	}
	if h.mode == writeMode && actual != "Exclusive" || h.mode != writeMode && actual != "Shared" {
		return errNotOwned
	}
	return nil
}

func (h *sqlOwnership) unlock(ctx context.Context) error {
	if err := sqlStatus(ctx, h.conn, sqlUnlock, h.name, h.bits); err != nil {
		if errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
			return discardSQLConnection(h.conn)
		}
		return errors.Join(err, discardSQLConnection(h.conn))
	}
	return h.conn.Close()
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
		return errBusy
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
