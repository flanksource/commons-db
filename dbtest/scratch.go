package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// maxIdentifier is PostgreSQL's identifier length limit (NAMEDATALEN - 1).
const maxIdentifier = 63

var (
	nonIdentifier = regexp.MustCompile(`[^a-z0-9]+`)
	scratchSeq    atomic.Uint64
)

// createLeasedDatabase carves a fresh managed test database out of the server
// adminURL points at, and returns a DSN for it plus a closure that drops it.
//
// The lease session locks the name before the database exists and holds it
// until cleanup, so a sweeper treats an unheld lock as an abandoned database.
// adminURL is used only as a maintenance connection; the database it names is
// never modified. The returned DSN is adminURL with its path replaced, so every
// other connection parameter (sslmode, credentials, options) is preserved.
func createLeasedDatabase(
	ctx context.Context,
	adminURL, name, unique string,
	now time.Time,
) (string, *databaseLease, error) {
	session, err := openPoolSession(ctx, adminURL)
	if err != nil {
		return "", nil, err
	}
	dbName := managedDatabaseName(testDatabasePrefix, now.UTC().Truncate(time.Second), unique, name)
	if err := session.lock(ctx, dbName); err != nil {
		return "", nil, errors.Join(err, session.close())
	}
	if err := session.createDatabase(ctx, dbName, ""); err != nil {
		return "", nil, errors.Join(fmt.Errorf("create database %s: %w", dbName, err), session.close())
	}
	lease := &databaseLease{session: session, name: dbName}
	dsn, err := withDatabase(adminURL, dbName)
	if err != nil {
		return "", nil, errors.Join(err, lease.release())
	}
	return dsn, lease, nil
}

// withDatabase returns dsn pointing at database instead of whatever it named.
//
// This parses the URL rather than substringing it: a DSN whose database is not
// literally "postgres", or whose host or password happens to contain the text
// of the old database name, would silently survive or be corrupted by a
// string replacement.
func withDatabase(dsn, database string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", redact(dsn), err)
	}
	u.Path = "/" + database
	return u.String(), nil
}

// sanitize reduces s to lowercase alphanumerics and underscores, so it is a
// legal identifier no matter what a test's name contains.
func sanitize(s string) string {
	s = nonIdentifier.ReplaceAllString(strings.ToLower(s), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "dbtest"
	}
	if len(s) > maxIdentifier {
		s = s[:maxIdentifier]
	}
	return s
}

// uniqueSuffix distinguishes concurrent resolutions sharing one server. The
// counter separates calls within a process and the random word separates
// processes — including processes on different machines pointed at the same
// COMMONS_DB_URL, where pids alone would collide.
func uniqueSuffix() string {
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		panic(fmt.Sprintf("dbtest: read entropy for database name: %v", err))
	}
	return fmt.Sprintf("%d_%s", scratchSeq.Add(1), hex.EncodeToString(entropy[:]))
}

// redact strips the password from a DSN so it is safe to put in an error.
func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<unparseable dsn>"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}
