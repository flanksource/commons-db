package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flanksource/commons-db/lease"
)

const (
	poolLeaseScope    = "git-pool"
	defaultPoolRetain = 30 * 24 * time.Hour
	poolPruneExpire   = "2.weeks.ago"
)

var leaseNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// poolsDir holds one directory per organization pool: PoolRoot when shared,
// else <Root>/v2/pools.
func (c *Cache) poolsDir() string {
	if c.PoolRoot != "" {
		return c.PoolRoot
	}
	return filepath.Join(c.cacheRoot(), "pools")
}

func (c *Cache) poolPath(name string) string { return filepath.Join(c.poolsDir(), name, "pool.git") }

// poolLocker is Cache.Locker, else flock files in the local pools directory.
// A shared pool never falls back to flock: across hosts it cannot detect a
// dead holder or signal a lost lock.
func (c *Cache) poolLocker() (lease.Locker, error) {
	if c.Locker != nil {
		return c.Locker, nil
	}
	if c.PoolRoot != "" {
		return nil, fmt.Errorf("shared git pool %s requires Cache.Locker", c.PoolRoot)
	}
	return lease.NewFile(c.poolsDir(), lease.Options{Scope: poolLeaseScope, RetryInterval: lockRetry})
}

func (c *Cache) poolRetain() time.Duration {
	if c.PoolRetain > 0 {
		return c.PoolRetain
	}
	return defaultPoolRetain
}

// poolLease names a lease on this mirror's pool: "" for the pool itself
// (shared by mirror writes, exclusive for maintenance), "fetch" and "init".
// Lease keys are at most 100 characters, so a long pool name keeps its last
// 88, which end in the pool's identity hash.
func (m mirrorPaths) poolLease(suffix string) string {
	name := leaseNameUnsafe.ReplaceAllString(m.poolName(), "_")
	name = "pool-" + name[max(0, len(name)-88):]
	if suffix != "" {
		name += "-" + suffix
	}
	return name
}

// holder names this process in pool lease reports: Cache.Holder, else
// POD_NAME, else <hostname>-<pid>.
func (c *Cache) holder() (string, error) {
	if c.Holder != "" {
		return c.Holder, nil
	}
	if pod := os.Getenv("POD_NAME"); pod != "" {
		return pod, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("name the git pool lease holder: %w", err)
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid()), nil
}

// holdLease acquires a lease and returns a context cancelled when the lease
// is lost, for the protected work, and its release. Release reports a loss
// (lease.ErrLost) with the unlock error.
func holdLease(ctx context.Context, what string, acquire func(context.Context) (lease.Lease, error)) (context.Context, func() error, error) {
	held, err := acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire %s: %w", what, err)
	}
	work, cancel := context.WithCancelCause(ctx)
	lost := func() bool {
		if err := held.Err(); err != nil {
			cancel(fmt.Errorf("%s: %w", what, err))
			return true
		}
		return false
	}
	stop := make(chan struct{})
	select {
	case <-held.Done():
		lost()
	default:
		go func() {
			select {
			case <-held.Done():
				lost()
			case <-stop:
			}
		}()
	}
	return work, func() error {
		close(stop)
		cancel(nil)
		var lostErr error
		if err := held.Err(); err != nil {
			lostErr = fmt.Errorf("%s: %w", what, err)
		}
		return errors.Join(lostErr, held.Unlock(context.WithoutCancel(ctx)))
	}, nil
}

// stampsDir holds, per repository key, when a cache last pinned, fetched or
// reconciled that repository in the pool: an RFC 3339 time, written by
// temp-and-rename so another host never reads a partial stamp.
func stampsDir(pool string) string { return filepath.Join(filepath.Dir(pool), "stamps") }

func writeStamp(pool, key string, used time.Time) error {
	if err := os.MkdirAll(stampsDir(pool), 0o755); err != nil {
		return fmt.Errorf("create git pool stamps: %w", err)
	}
	return replaceFile(filepath.Join(stampsDir(pool), key), []byte(used.UTC().Format(time.RFC3339Nano)))
}

func readStamp(pool, key string) (time.Time, bool, error) {
	path := filepath.Join(stampsDir(pool), key)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	used, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("git pool stamp %s: %w", path, err)
	}
	return used, true, nil
}

// verifyAlternate refuses a mirror that borrows from anywhere but this
// pool, and rewrites a relative alternate of this pool as absolute.
func (m mirrorPaths) verifyAlternate() error {
	want, err := filepath.Abs(filepath.Join(m.pool, "objects"))
	if err != nil {
		return err
	}
	path := filepath.Join(m.git, "objects", "info", "alternates")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read git mirror alternates %s: %w", path, err)
	}
	found := strings.TrimSpace(string(data))
	resolved := found
	if !filepath.IsAbs(found) {
		resolved = filepath.Join(m.git, "objects", found)
	}
	if filepath.Clean(resolved) != want {
		return fmt.Errorf("git mirror %s borrows objects from %s, not from the pool %s", m.git, filepath.Clean(resolved), want)
	}
	if found == want {
		return nil
	}
	return replaceFile(path, []byte(want+"\n"))
}
