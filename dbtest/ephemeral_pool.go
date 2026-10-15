package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
)

const poolLockNamespace = "commons-db:ephemeral-pool:"

const databaseConnectionPollInterval = 10 * time.Millisecond

type poolSession struct {
	db   *sql.DB
	conn *sql.Conn
}

type databaseLease struct {
	session *poolSession
	name    string
	once    sync.Once
	err     error
}

func (p *ephemeralPool) acquire(
	ctx context.Context,
	name, unique string,
) (string, *databaseLease, error) {
	if name == "" {
		return "", nil, errors.New("ephemeral database name is required")
	}
	if unique == "" {
		return "", nil, errors.New("ephemeral database unique suffix is required")
	}
	session, err := openPoolSession(ctx, p.config.AdminURL)
	if err != nil {
		return "", nil, err
	}

	// Closing the session releases every advisory lock it holds, and checkout
	// renames nothing unless it returns a lease, so failures only close it.
	if err := session.lock(ctx, p.globalLock()); err != nil {
		return "", nil, errors.Join(err, session.close())
	}
	leaseName, err := p.checkout(ctx, session, name, unique)
	if err != nil {
		return "", nil, errors.Join(err, session.close())
	}
	lease := &databaseLease{session: session, name: leaseName}
	if err := session.unlock(ctx, p.globalLock()); err != nil {
		return "", nil, errors.Join(err, lease.release())
	}

	dsn, err := withDatabase(p.config.AdminURL, leaseName)
	if err != nil {
		return "", nil, errors.Join(err, lease.release())
	}
	return dsn, lease, nil
}

func (p *ephemeralPool) globalLock() string {
	return poolLockNamespace + p.config.PoolPrefix + p.config.TestPrefix
}

func openPoolSession(ctx context.Context, adminURL string) (*poolSession, error) {
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		return nil, fmt.Errorf("open ephemeral database pool %s: %w", redact(adminURL), err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("connect ephemeral database pool %s: %w", redact(adminURL), err),
			db.Close(),
		)
	}
	return &poolSession{db: db, conn: conn}, nil
}

func (s *poolSession) lock(ctx context.Context, name string) error {
	if _, err := s.conn.ExecContext(
		ctx,
		"SELECT pg_advisory_lock(hashtextextended($1, 0))",
		name,
	); err != nil {
		return fmt.Errorf("acquire PostgreSQL advisory lock %q: %w", name, err)
	}
	return nil
}

func (s *poolSession) tryLock(ctx context.Context, name string) (bool, error) {
	var locked bool
	if err := s.conn.QueryRowContext(
		ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))",
		name,
	).Scan(&locked); err != nil {
		return false, fmt.Errorf("try PostgreSQL advisory lock %q: %w", name, err)
	}
	return locked, nil
}

func (s *poolSession) unlock(ctx context.Context, name string) error {
	var unlocked bool
	if err := s.conn.QueryRowContext(
		ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))",
		name,
	).Scan(&unlocked); err != nil {
		return fmt.Errorf("release PostgreSQL advisory lock %q: %w", name, err)
	}
	if !unlocked {
		return fmt.Errorf("release PostgreSQL advisory lock %q: lock was not held", name)
	}
	return nil
}

func (s *poolSession) close() error {
	return errors.Join(s.conn.Close(), s.db.Close())
}

func (p *ephemeralPool) checkout(
	ctx context.Context,
	session *poolSession,
	name, unique string,
) (string, error) {
	now := p.config.Now().UTC().Truncate(time.Second)
	free, err := p.freeDatabases(ctx, session)
	if err != nil {
		return "", err
	}
	if len(free) == 0 {
		if err := p.createBatch(ctx, session, now); err != nil {
			return "", err
		}
		free, err = p.freeDatabases(ctx, session)
		if err != nil {
			return "", err
		}
	}
	if len(free) == 0 {
		return "", errors.New("ephemeral database batch creation produced no free databases")
	}

	// The lease is locked before it exists, so a sweeper can treat an unheld
	// lock on any test database as proof that its owner is gone.
	leaseName := managedDatabaseName(p.config.TestPrefix, now, unique, name)
	if err := session.lock(ctx, leaseName); err != nil {
		return "", err
	}
	if _, err := session.conn.ExecContext(
		ctx,
		"ALTER DATABASE "+pq.QuoteIdentifier(free[0])+" RENAME TO "+pq.QuoteIdentifier(leaseName),
	); err != nil {
		return "", fmt.Errorf("check out ephemeral database %s: %w", leaseName, err)
	}
	return leaseName, nil
}

func (p *ephemeralPool) createBatch(
	ctx context.Context,
	session *poolSession,
	now time.Time,
) error {
	for i := 0; i < p.config.BatchSize; i++ {
		name := managedDatabaseName(p.config.PoolPrefix, now, uniqueSuffix(), "")
		if _, err := session.conn.ExecContext(
			ctx,
			"CREATE DATABASE "+pq.QuoteIdentifier(name),
		); err != nil {
			return fmt.Errorf("create pooled database %s: %w", name, err)
		}
	}
	return nil
}

func (p *ephemeralPool) freeDatabases(
	ctx context.Context,
	session *poolSession,
) ([]string, error) {
	rows, err := session.conn.QueryContext(
		ctx,
		"SELECT datname FROM pg_database WHERE left(datname, length($1)) = $1 ORDER BY datname",
		p.config.PoolPrefix,
	)
	if err != nil {
		return nil, fmt.Errorf("list pooled databases: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan pooled database name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pooled databases: %w", err)
	}
	return names, nil
}

func (p *ephemeralPool) managedDatabases(
	ctx context.Context,
	session *poolSession,
) ([]string, error) {
	rows, err := session.conn.QueryContext(
		ctx,
		`SELECT datname
		 FROM pg_database
		 WHERE left(datname, length($1)) = $1
		    OR left(datname, length($2)) = $2
		 ORDER BY datname`,
		p.config.PoolPrefix,
		p.config.TestPrefix,
	)
	if err != nil {
		return nil, fmt.Errorf("list managed ephemeral databases: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan managed ephemeral database: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list managed ephemeral databases: %w", err)
	}
	return names, nil
}

func (s *poolSession) databaseHasConnections(ctx context.Context, name string) (bool, error) {
	var active bool
	if err := s.conn.QueryRowContext(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = $1)",
		name,
	).Scan(&active); err != nil {
		return false, fmt.Errorf("inspect sessions for database %s: %w", name, err)
	}
	return active, nil
}

func (s *poolSession) waitForDatabaseConnectionsToClose(ctx context.Context, name string) error {
	ticker := time.NewTicker(databaseConnectionPollInterval)
	defer ticker.Stop()
	for {
		active, err := s.databaseHasConnections(ctx, name)
		if err != nil || !active {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// dropDatabase is never cancelled once sent: PostgreSQL 15+ marks a database
// whose drop is interrupted as invalid, leaving it unusable until dropped again.
func (s *poolSession) dropDatabase(ctx context.Context, name string, force bool) error {
	statement := "DROP DATABASE IF EXISTS " + pq.QuoteIdentifier(name)
	if force {
		statement += " WITH (FORCE)"
	}
	if _, err := s.conn.ExecContext(context.WithoutCancel(ctx), statement); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

// release drops the leased database and waits for the result.
func (l *databaseLease) release() error {
	l.once.Do(func() { l.err = l.drop(nil) })
	return l.err
}

// releaseInBackground returns as soon as DROP DATABASE has been written to the
// server and finishes the drop in a goroutine. The drop runs outside a
// transaction, so the server completes it even if this process exits before the
// result arrives (unless the server sets client_connection_check_interval);
// a drop that never reaches the server leaves an unlocked database for the
// next sweep.
func (l *databaseLease) releaseInBackground() {
	sent := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.once.Do(func() { l.err = l.drop(sent) })
		if l.err != nil {
			logger.Warnf("dbtest: drop database %s: %v", l.name, l.err)
		}
	}()
	select {
	case <-sent:
	case <-done:
	}
}

// drop closes sent, when given, once the statement is flushed to the server.
func (l *databaseLease) drop(sent chan<- struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	statement := "DROP DATABASE IF EXISTS " + pq.QuoteIdentifier(l.name) + " WITH (FORCE)"
	dropErr := l.session.conn.Raw(func(driverConn any) error {
		conn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected database driver connection %T", driverConn)
		}
		// Not cancellable for the same reason as poolSession.dropDatabase.
		result := conn.Conn().PgConn().Exec(context.WithoutCancel(ctx), statement)
		if sent != nil {
			close(sent)
		}
		results, err := result.ReadAll()
		for _, r := range results {
			err = errors.Join(err, r.Err)
		}
		return err
	})
	if dropErr != nil {
		dropErr = fmt.Errorf("drop database %s: %w", l.name, dropErr)
	}
	return errors.Join(dropErr, l.session.unlock(ctx, l.name), l.session.close())
}

func isObjectInUse(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ObjectInUse
}
