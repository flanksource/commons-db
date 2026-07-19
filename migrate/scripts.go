package migrate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/lib/pq"
)

const (
	// migrationLockTimeout bounds how long a transactional migration waits for a
	// table lock before Postgres cancels it (55P03). This turns an indefinite
	// block behind a live reader (e.g. a running server SELECT-ing the same
	// captain_* tables) into a retryable timeout instead of a hang or deadlock.
	migrationLockTimeout = "15s"
	// migrationMaxAttempts is the total number of times a transactional migration
	// is tried before giving up when it keeps hitting lock contention.
	migrationMaxAttempts = 6
	// migrationRetryBackoff is the base delay between retries; it grows linearly
	// with the attempt number so a busy server gets progressively longer gaps.
	migrationRetryBackoff = 250 * time.Millisecond
)

type scriptPhase string

const (
	phasePre  scriptPhase = "pre"
	phasePost scriptPhase = "post"
)

type script struct {
	path          string
	content       string
	phase         scriptPhase
	dependsOn     []string
	always        bool
	transactional bool
	hash          []byte
}

func loadScripts(schemaFS fs.FS, dir string) (map[string]*script, error) {
	scripts := map[string]*script{}
	err := fs.WalkDir(schemaFS, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.ToLower(path.Ext(name)) != ".sql" {
			return nil
		}
		data, err := fs.ReadFile(schemaFS, name)
		if err != nil {
			return fmt.Errorf("read SQL %s: %w", name, err)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(name, dir), "/")
		if rel == "" {
			return fmt.Errorf("invalid SQL script path %q", name)
		}
		s, err := parseScript(rel, string(data))
		if err != nil {
			return err
		}
		if _, exists := scripts[s.path]; exists {
			return fmt.Errorf("duplicate SQL script path %q", s.path)
		}
		scripts[s.path] = s
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load SQL migrations: %w", err)
	}
	if err := validateScriptGraph(scripts); err != nil {
		return nil, err
	}
	return scripts, nil
}

func parseScript(name, content string) (*script, error) {
	s := &script{path: path.Clean(name), content: content, phase: phasePost, transactional: true}
	sum := sha256.Sum256([]byte(content))
	s.hash = sum[:]

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "--") {
			break
		}
		switch {
		case strings.HasPrefix(line, "-- phase:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "-- phase:"))
			switch scriptPhase(value) {
			case phasePre, phasePost:
				s.phase = scriptPhase(value)
			default:
				return nil, fmt.Errorf("SQL script %q has invalid phase %q", name, value)
			}
		case strings.HasPrefix(line, "-- dependsOn:"):
			for _, dep := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "-- dependsOn:")), ",") {
				dep = path.Clean(strings.TrimSpace(dep))
				if dep != "." && dep != "" {
					s.dependsOn = append(s.dependsOn, dep)
				}
			}
		case line == "-- runs: always":
			s.always = true
		case strings.HasPrefix(line, "-- transaction:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "-- transaction:"))
			switch value {
			case "true":
				s.transactional = true
			case "false":
				s.transactional = false
			default:
				return nil, fmt.Errorf("SQL script %q has invalid transaction value %q", name, value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SQL script %q: %w", name, err)
	}
	return s, nil
}

func validateScriptGraph(scripts map[string]*script) error {
	for _, s := range scripts {
		seen := map[string]bool{}
		for _, dep := range s.dependsOn {
			if seen[dep] {
				return fmt.Errorf("SQL script %q declares dependency %q more than once", s.path, dep)
			}
			seen[dep] = true
			d, ok := scripts[dep]
			if !ok {
				return fmt.Errorf("SQL script %q depends on missing script %q", s.path, dep)
			}
			if s.phase == phasePre && d.phase == phasePost {
				return fmt.Errorf("pre SQL script %q cannot depend on post script %q", s.path, dep)
			}
		}
	}
	_, err := topologicalScripts(scripts, nil)
	return err
}

func topologicalScripts(scripts map[string]*script, selected map[string]bool) ([]*script, error) {
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for name, s := range scripts {
		if selected != nil && !selected[name] {
			continue
		}
		for _, dep := range s.dependsOn {
			if selected != nil && !selected[dep] {
				continue
			}
			indegree[name]++
			dependents[dep] = append(dependents[dep], name)
		}
		if _, ok := indegree[name]; !ok {
			indegree[name] = 0
		}
	}
	ready := make([]string, 0)
	for name, degree := range indegree {
		if degree == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	ordered := make([]*script, 0, len(indegree))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		ordered = append(ordered, scripts[name])
		for _, dependent := range dependents[name] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(indegree) {
		var cyclic []string
		for name, degree := range indegree {
			if degree > 0 {
				cyclic = append(cyclic, name)
			}
		}
		sort.Strings(cyclic)
		return nil, fmt.Errorf("SQL migration dependency cycle: %s", strings.Join(cyclic, ", "))
	}
	return ordered, nil
}

func ensureMetadataTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migration_scripts (
  scope text NOT NULL,
  path text NOT NULL,
  hash bytea NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, path)
);
CREATE TABLE IF NOT EXISTS schema_migration_security (
  scope text PRIMARY KEY,
  state jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
)`)
	if err != nil {
		return fmt.Errorf("create migration metadata tables: %w", err)
	}
	return nil
}

func selectScripts(ctx context.Context, db *sql.DB, scope string, scripts map[string]*script) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, hash FROM schema_migration_scripts WHERE scope = $1`, scope)
	if err != nil {
		return nil, fmt.Errorf("read SQL migration hashes: %w", err)
	}
	defer rows.Close()
	hashes := map[string][]byte{}
	for rows.Next() {
		var name string
		var hash []byte
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, err
		}
		hashes[name] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for name, s := range scripts {
		selected[name] = s.always || !equalBytes(hashes[name], s.hash)
	}
	changed := true
	for changed {
		changed = false
		for name, s := range scripts {
			if selected[name] {
				continue
			}
			for _, dep := range s.dependsOn {
				if selected[dep] {
					selected[name] = true
					changed = true
					break
				}
			}
		}
	}
	return selected, nil
}

func runScriptPhase(ctx context.Context, db *sql.DB, scope string, ordered []*script, phase scriptPhase) error {
	for _, s := range ordered {
		if s.phase != phase {
			continue
		}
		if s.transactional {
			if err := runTransactionalScript(ctx, db, scope, s); err != nil {
				return err
			}
			continue
		}
		if _, err := db.ExecContext(ctx, s.content); err != nil {
			return fmt.Errorf("execute non-transactional SQL migration %s: %w", s.path, err)
		}
		if err := recordScript(ctx, db, scope, s); err != nil {
			return err
		}
	}
	return nil
}

// retryOnLockContention runs fn, retrying on transient lock contention (a
// detected deadlock, a lock_timeout, or a serialization failure) with linear
// backoff. A DDL statement (a transactional script, a dependent-view DROP, or a
// security reconciliation) takes ACCESS EXCLUSIVE on tables a concurrently
// running process may be reading; the bounded lock_timeout converts the
// resulting hang into a retryable 55P03, and a deadlock (40P01) aborts one party
// — either way retrying lets the migration win once the reader's short query
// completes, instead of failing the whole run. fn MUST be idempotent.
func retryOnLockContention(ctx context.Context, desc string, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= migrationMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableMigrationErr(err) {
			return err
		}
		lastErr = err
		if attempt < migrationMaxAttempts {
			logger.Debugf("%s hit lock contention (attempt %d/%d): %v", desc, attempt, migrationMaxAttempts, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * migrationRetryBackoff):
			}
		}
	}
	return fmt.Errorf("gave up after %d attempts under lock contention: %w", migrationMaxAttempts, lastErr)
}

// runTransactionalScript applies one transactional migration under a bounded
// lock_timeout, retrying on lock contention. The view/trigger scripts split out
// of the historical 50_views_and_triggers monolith are the canonical DDL that
// contends with live readers.
func runTransactionalScript(ctx context.Context, db *sql.DB, scope string, s *script) error {
	return retryOnLockContention(ctx, "execute SQL migration "+s.path, func() error {
		return applyTransactionalScript(ctx, db, scope, s)
	})
}

// applyTransactionalScript runs the script (and records it) in a single
// transaction with a bounded lock_timeout so it never blocks indefinitely.
func applyTransactionalScript(ctx context.Context, db *sql.DB, scope string, s *script) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQL migration %s: %w", s.path, err)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set lock_timeout for SQL migration %s: %w", s.path, err)
	}
	if _, err := tx.ExecContext(ctx, s.content); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("execute SQL migration %s: %w", s.path, err)
	}
	if err := recordScript(ctx, tx, scope, s); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQL migration %s: %w", s.path, err)
	}
	return nil
}

// isRetryableMigrationErr reports whether err is transient lock contention that a
// retry can clear: a detected deadlock, a lock_timeout, or a serialization
// failure. A wrapped *pq.Error carries the SQLSTATE that classifies it.
func isRetryableMigrationErr(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	switch pqErr.Code {
	case "40P01", // deadlock_detected
		"55P03", // lock_not_available (lock_timeout fired)
		"40001": // serialization_failure
		return true
	default:
		return false
	}
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func recordScript(ctx context.Context, db execer, scope string, s *script) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO schema_migration_scripts(scope, path, hash, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (scope, path) DO UPDATE SET hash = EXCLUDED.hash, updated_at = now()`, scope, s.path, s.hash)
	if err != nil {
		return fmt.Errorf("record SQL migration %s: %w", s.path, err)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
