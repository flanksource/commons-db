package snapshots

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons/logger"
)

// reloadLocked rebuilds the snapshots a previous process left on disk.
//
// A snapshot is a link someone can bookmark, so a restart that silently voided
// every one of them would make the results URL a lie about its own lifetime.
// What a restart must never do is the opposite — resurrect something a browser
// was already told had expired — which is why the persisted deadline is
// honoured here rather than recomputed.
//
// A directory that cannot be understood is removed rather than skipped: it is
// unreachable either way, and leaving it would accumulate dead files no prune
// pass ever claims.
func (m *Manager) reloadLocked() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("read snapshot directory: %w", err)
	}
	now := m.now()
	for _, entry := range entries {
		// Only directories this manager writes are candidates; a stray file is
		// left alone rather than deleted on a guess.
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.dir, entry.Name())
		item, err := openSnapshotDir(dir, entry.Name())
		if err != nil {
			logger.Warnf("discarding unreadable reconciliation snapshot %s: %v", entry.Name(), err)
			_ = os.RemoveAll(dir)
			continue
		}
		if !now.Before(item.expiresAt) {
			// Tombstone before deleting, so a bookmarked link that outlived its
			// snapshot is answered "expired" rather than "never existed".
			m.tombstoneLocked(item.snapshot)
			_ = item.snapshot.db.Close()
			_ = os.RemoveAll(dir)
			continue
		}
		// The operator may have lowered the server maximum while this snapshot
		// was on disk; it cannot outlive the new ceiling.
		if item.snapshot.age > m.maxAge {
			item.snapshot.age = m.maxAge
		}
		// lastAccessed is not durable, and a window in which nobody *could* look
		// at a snapshot is not evidence that nobody wants it — so the idle clock
		// restarts. The persisted deadline above is what bounds the total life.
		item.snapshot.lastAccessed = now
		item.snapshot.connection.ExpiresAt = ptrTime(now.Add(item.snapshot.age))
		m.items[item.snapshot.id] = item.snapshot
		for name := range item.snapshot.profiles {
			m.profiles[name] = item.snapshot.id
		}
	}
	return nil
}

type reloadedSnapshot struct {
	snapshot  *snapshot
	expiresAt time.Time
}

// openSnapshotDir rebuilds one snapshot from its file, or reports why it cannot
// be trusted. Every inconsistency is fatal to that snapshot rather than
// repaired: half a reconciliation is not a reconciliation.
func openSnapshotDir(dir, id string) (reloadedSnapshot, error) {
	path := filepath.Join(dir, "snapshot.sqlite")
	if _, err := os.Stat(path); err != nil {
		return reloadedSnapshot{}, fmt.Errorf("no snapshot database: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return reloadedSnapshot{}, fmt.Errorf("open snapshot: %w", err)
	}
	// sql.Open is lazy, so this read is also the corruption check — a truncated
	// file left by a killed process fails here rather than on first use.
	meta, err := readSnapshotMetadata(context.Background(), database)
	if err != nil {
		_ = database.Close()
		return reloadedSnapshot{}, err
	}
	if meta.ID != id {
		_ = database.Close()
		return reloadedSnapshot{}, fmt.Errorf("metadata names snapshot %q in directory %q", meta.ID, id)
	}
	if len(meta.Profiles) == 0 || meta.BaseProfile == "" {
		_ = database.Close()
		return reloadedSnapshot{}, fmt.Errorf("snapshot names no base profile")
	}
	connectionID, err := uuid.Parse(meta.ConnectionID)
	if err != nil {
		_ = database.Close()
		return reloadedSnapshot{}, fmt.Errorf("snapshot connection id %q: %w", meta.ConnectionID, err)
	}

	item := &snapshot{
		id:          meta.ID,
		path:        path,
		db:          database,
		createdAt:   meta.CreatedAt,
		age:         meta.Age,
		connection:  snapshotConnection(connectionID, meta.ConnectionName, path, meta.CreatedAt),
		stats:       meta.Stats,
		source:      meta.Source,
		dest:        meta.Dest,
		sourceCut:   meta.SourceTruncated,
		destCut:     meta.DestTruncated,
		baseProfile: meta.BaseProfile,
		reconcile:   meta.Reconcile,
		profiles:    make(map[string]materialization, len(meta.Profiles)),
	}
	if meta.ConnectionNamespace != "" {
		item.connection.Namespace = meta.ConnectionNamespace
	}
	for _, stored := range meta.Profiles {
		if err := tableExists(database, stored.Table); err != nil {
			_ = database.Close()
			return reloadedSnapshot{}, fmt.Errorf("snapshot profile %q: %w", stored.Name, err)
		}
		// The generated profile is recomputed rather than restored, so the
		// running binary stays the single authority on how a snapshot is queried.
		item.profiles[stored.Name] = materialization{
			profile: snapshotProfile(stored.Name, stored.Table, stored.Columns, meta.ConnectionName, stored.Rows),
			table:   stored.Table,
			columns: stored.Columns,
			rows:    stored.Rows,
		}
	}
	if _, found := item.profiles[meta.BaseProfile]; !found {
		_ = database.Close()
		return reloadedSnapshot{}, fmt.Errorf("base profile %q is not among the stored profiles", meta.BaseProfile)
	}
	return reloadedSnapshot{snapshot: item, expiresAt: meta.ExpiresAt}, nil
}

func tableExists(database *sql.DB, table string) error {
	var name string
	err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if err != nil {
		return fmt.Errorf("table %q is missing from the snapshot: %w", table, err)
	}
	return nil
}
