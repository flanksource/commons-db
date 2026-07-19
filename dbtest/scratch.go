package dbtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/lib/pq"
)

// maxIdentifier is PostgreSQL's identifier length limit (NAMEDATALEN - 1).
const maxIdentifier = 63

var (
	nonIdentifier = regexp.MustCompile(`[^a-z0-9]+`)
	scratchSeq    atomic.Uint64
)

// createScratch carves a fresh database out of the server adminURL points at,
// and returns a DSN for it plus a closure that drops it.
//
// adminURL is used only as a maintenance connection; the database it names is
// never modified. The returned DSN is adminURL with its path replaced, so every
// other connection parameter (sslmode, credentials, options) is preserved.
func createScratch(adminURL, name, unique string) (string, func() error, error) {
	dbName := scratchName(name, unique)

	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		return "", nil, fmt.Errorf("open %s: %w", redact(adminURL), err)
	}
	defer admin.Close() //nolint:errcheck

	quoted := pq.QuoteIdentifier(dbName)
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + quoted + ` WITH (FORCE)`); err != nil {
		return "", nil, fmt.Errorf("drop stale database %s: %w", dbName, err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + quoted); err != nil {
		return "", nil, fmt.Errorf("create database %s: %w", dbName, err)
	}

	dsn, err := withDatabase(adminURL, dbName)
	if err != nil {
		return "", nil, err
	}

	return dsn, func() error {
		cleanup, err := sql.Open("postgres", adminURL)
		if err != nil {
			return fmt.Errorf("open %s to drop %s: %w", redact(adminURL), dbName, err)
		}
		defer cleanup.Close() //nolint:errcheck
		if _, err := cleanup.Exec(`DROP DATABASE IF EXISTS ` + quoted + ` WITH (FORCE)`); err != nil {
			return fmt.Errorf("drop database %s: %w", dbName, err)
		}
		return nil
	}, nil
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

// scratchName builds a unique, legal database identifier. When the combined
// name would exceed PostgreSQL's limit the *base* is truncated, never the
// unique suffix — trimming the tail is what makes two concurrent runs collide.
func scratchName(base, unique string) string {
	name := sanitize(base)
	if room := maxIdentifier - len(unique) - 1; len(name) > room {
		name = name[:room]
	}
	return name + "_" + unique
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
