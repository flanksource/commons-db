package dbtest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

const sweepTimeout = 2 * time.Minute

// abandonedDatabaseGrace is how old a test database must be before an unheld
// lease lock is taken as proof that its owner is gone. Current owners lock the
// name before the database exists, so this only protects releases that created
// or renamed the database first and locked it afterwards, at most one
// provisioning window later.
const abandonedDatabaseGrace = provisioningTimeout + connectTimeout

var activeSweeps sync.Map

// sweepInBackground runs sweep unless one with the same key is already in
// flight. Sweeping is best effort: a failure is logged and never fails the
// checkout that triggered it, and anything it misses is retried by the next.
func sweepInBackground(key string, sweep func(context.Context) error) {
	if _, running := activeSweeps.LoadOrStore(key, struct{}{}); running {
		return
	}
	go func() {
		defer activeSweeps.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
		defer cancel()
		if err := sweep(ctx); err != nil {
			logger.Warnf("dbtest: sweep %s: %v", key, err)
		}
	}()
}

// sweepManagedDatabases removes abandoned test databases and expired free pool
// databases from the server adminURL points at.
func sweepManagedDatabases(adminURL string) {
	pool := &ephemeralPool{config: defaultPoolConfig(adminURL, time.Now)}
	sweepInBackground("databases "+redact(adminURL), pool.sweep)
}

// sweep drops every test database whose lease lock is free, and every free
// pool database older than MaxAge.
func (p *ephemeralPool) sweep(ctx context.Context) error {
	session, err := openPoolSession(ctx, p.config.AdminURL)
	if err != nil {
		return err
	}
	defer session.close() //nolint:errcheck

	names, err := p.managedDatabases(ctx, session)
	if err != nil {
		return err
	}
	now := p.config.Now().UTC()
	var failures error
	var expired []string
	for _, name := range names {
		created, err := managedDatabaseCreatedWithPrefixes(name, p.config.PoolPrefix, p.config.TestPrefix)
		if err != nil || created.After(now) {
			logger.Warnf("dbtest: skipping managed database %q with an invalid or future timestamp", name)
			continue
		}
		switch {
		case strings.HasPrefix(name, p.config.TestPrefix):
			if now.Sub(created) > abandonedDatabaseGrace {
				failures = errors.Join(failures, sweepAbandonedDatabase(ctx, session, name))
			}
		case now.Sub(created) > p.config.MaxAge:
			expired = append(expired, name)
		}
	}
	return errors.Join(failures, p.sweepExpiredPoolDatabases(ctx, session, expired))
}

// sweepAbandonedDatabase drops a test database whose owner no longer holds its
// lease lock. A held lock means a live test, so it is skipped.
func sweepAbandonedDatabase(ctx context.Context, session *poolSession, name string) error {
	locked, err := session.tryLock(ctx, name)
	if err != nil || !locked {
		return err
	}
	return errors.Join(session.dropDatabase(ctx, name, true), session.unlock(ctx, name))
}

// sweepExpiredPoolDatabases drops free pool databases while holding the pool
// lock, so a concurrent checkout cannot rename one away mid-drop. A busy pool is
// skipped rather than waited on.
func (p *ephemeralPool) sweepExpiredPoolDatabases(
	ctx context.Context,
	session *poolSession,
	names []string,
) error {
	if len(names) == 0 {
		return nil
	}
	globalLock := p.globalLock()
	locked, err := session.tryLock(ctx, globalLock)
	if err != nil || !locked {
		return err
	}
	var failures error
	for _, name := range names {
		active, err := session.databaseHasConnections(ctx, name)
		if err == nil && !active {
			err = session.dropDatabase(ctx, name, false)
		}
		if !isObjectInUse(err) {
			failures = errors.Join(failures, err)
		}
	}
	return errors.Join(failures, session.unlock(ctx, globalLock))
}
