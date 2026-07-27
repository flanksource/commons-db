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
	"github.com/lib/pq"
)

const poolLockNamespace = "commons-db:ephemeral-pool:"

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
) (string, func() error, error) {
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

	globalLock := poolLockNamespace + p.config.PoolPrefix + p.config.TestPrefix
	if err := session.lock(ctx, globalLock); err != nil {
		return "", nil, errors.Join(err, session.close())
	}
	leaseName, err := p.checkout(ctx, session, name, unique)
	if err != nil {
		return "", nil, errors.Join(err, session.unlock(ctx, globalLock), session.close())
	}
	if err := session.lock(ctx, leaseName); err != nil {
		dropErr := session.dropDatabase(ctx, leaseName, true)
		return "", nil, errors.Join(err, dropErr, session.unlock(ctx, globalLock), session.close())
	}
	if err := session.unlock(ctx, globalLock); err != nil {
		lease := &databaseLease{session: session, name: leaseName}
		return "", nil, errors.Join(err, lease.cleanup())
	}

	dsn, err := withDatabase(p.config.AdminURL, leaseName)
	if err != nil {
		lease := &databaseLease{session: session, name: leaseName}
		return "", nil, errors.Join(err, lease.cleanup())
	}
	lease := &databaseLease{session: session, name: leaseName}
	return dsn, lease.cleanup, nil
}

func openPoolSession(ctx context.Context, adminURL string) (*poolSession, error) {
	db, err := sql.Open("postgres", adminURL)
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
	if err := p.cleanupStale(ctx, session, now); err != nil {
		return "", err
	}
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

	leaseName := managedDatabaseName(p.config.TestPrefix, now, unique, name)
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

func (p *ephemeralPool) cleanupStale(
	ctx context.Context,
	session *poolSession,
	now time.Time,
) error {
	names, err := p.managedDatabases(ctx, session)
	if err != nil {
		return err
	}
	for _, name := range names {
		// An unparseable or future-dated entry is skipped rather than fatal.
		// cleanupStale runs under the pool-wide advisory lock, so aborting here
		// would wedge every checkout for this prefix pair until someone cleaned
		// up by hand - and a clock step-back is enough to produce one.
		created, err := managedDatabaseCreatedWithPrefixes(
			name,
			p.config.PoolPrefix,
			p.config.TestPrefix,
		)
		if err != nil {
			logger.Warnf("dbtest: skipping unrecognised managed database %q: %v", name, err)
			continue
		}
		if created.After(now) {
			logger.Warnf("dbtest: skipping managed database %q with a future timestamp %s", name, created)
			continue
		}
		if now.Sub(created) <= p.config.MaxAge {
			continue
		}
		if err := p.cleanupStaleDatabase(ctx, session, name); err != nil {
			return err
		}
	}
	return nil
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

func (p *ephemeralPool) cleanupStaleDatabase(
	ctx context.Context,
	session *poolSession,
	name string,
) error {
	leaseLocked := false
	if len(name) >= len(p.config.TestPrefix) &&
		name[:len(p.config.TestPrefix)] == p.config.TestPrefix {
		var err error
		leaseLocked, err = session.tryLock(ctx, name)
		if err != nil {
			return err
		}
		if !leaseLocked {
			return nil
		}
	}

	active, err := session.databaseHasConnections(ctx, name)
	if err == nil && !active {
		err = session.dropDatabase(ctx, name, false)
	}
	if isObjectInUse(err) {
		err = nil
	}
	if leaseLocked {
		err = errors.Join(err, session.unlock(ctx, name))
	}
	return err
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

func (s *poolSession) dropDatabase(ctx context.Context, name string, force bool) error {
	statement := "DROP DATABASE IF EXISTS " + pq.QuoteIdentifier(name)
	if force {
		statement += " WITH (FORCE)"
	}
	if _, err := s.conn.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

func (l *databaseLease) cleanup() error {
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		l.err = errors.Join(
			l.session.dropDatabase(ctx, l.name, true),
			l.session.unlock(ctx, l.name),
			l.session.close(),
		)
	})
	return l.err
}

func isObjectInUse(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == pgerrcode.ObjectInUse
}
