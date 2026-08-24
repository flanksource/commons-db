package snapshots

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query"
)

const (
	// metadataTable lives inside the snapshot's own SQLite file rather than
	// beside it, so a snapshot is one artefact: the rows and the record of how
	// they came to be cannot be separated, copied apart, or fall out of step.
	//
	// The leading underscore keeps it clear of the reconcile_rows and
	// materialized_* namespace. Nothing enumerates tables in the file and every
	// generated profile selects a fixed table by name, so it is unreachable
	// through the virtual profile.
	metadataTable = "_metadata"

	snapshotMetadataVersion = 1
)

// snapshotMetadata is everything Prepare needs to rebuild a *snapshot with no
// other source of truth.
//
// It stores what cannot be recomputed and omits what can. The generated
// query.Profile is the notable omission: its query embeds the physical
// "c0" AS name alias scheme, so persisting it would let a later build serve a
// stale — possibly wrong — SELECT after a restart. Recomputing keeps the
// running binary the single authority on the generated profile, exactly as it
// is before a restart.
type snapshotMetadata struct {
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	ExpiresAt time.Time     `json:"expires_at"`
	Age       time.Duration `json:"age"`

	// The connection uuid is stored rather than regenerated: a new one would
	// orphan every cached connection:// reference to this snapshot.
	ConnectionID        string `json:"connection_id"`
	ConnectionName      string `json:"connection_name"`
	ConnectionNamespace string `json:"connection_namespace"`

	// BaseProfile names which of Profiles is the reconciliation itself rather
	// than a projection of it. With N materializations the map alone cannot say.
	BaseProfile     string               `json:"base_profile"`
	Stats           query.ReconcileStats `json:"stats"`
	Source          string               `json:"source"`
	Dest            string               `json:"dest"`
	SourceTruncated bool                 `json:"source_truncated,omitempty"`
	DestTruncated   bool                 `json:"dest_truncated,omitempty"`

	Reconcile *profiles.ReconcileSnapshotProvenance `json:"reconcile,omitempty"`

	Profiles []snapshotProfileMetadata `json:"profiles"`
}

type snapshotProfileMetadata struct {
	Name    string            `json:"name"`
	Table   string            `json:"table"`
	Columns []query.ColumnDef `json:"columns"`
	Rows    int               `json:"rows"`
}

// writeSnapshotMetadata replaces the stored record. The delete and the insert
// share one transaction so a crash mid-write leaves the previous document
// intact rather than no document at all.
func writeSnapshotMetadata(ctx context.Context, database *sql.DB, meta snapshotMetadata) error {
	document, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode snapshot metadata: %w", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write snapshot metadata: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (version INTEGER NOT NULL, document TEXT NOT NULL)`,
			quoteIdentifier(metadataTable)),
		fmt.Sprintf(`DELETE FROM %s`, quoteIdentifier(metadataTable)),
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("write snapshot metadata: %w", err)
		}
	}
	insert := fmt.Sprintf(`INSERT INTO %s (version, document) VALUES (?, ?)`, quoteIdentifier(metadataTable))
	if _, err := transaction.ExecContext(ctx, insert, snapshotMetadataVersion, string(document)); err != nil {
		return fmt.Errorf("write snapshot metadata: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("write snapshot metadata: %w", err)
	}
	return nil
}

// readSnapshotMetadata reads the stored record. Because sql.Open is lazy, this
// query is also the first thing to touch the file — so it doubles as the
// corruption check for a directory left behind by a killed process.
func readSnapshotMetadata(ctx context.Context, database *sql.DB) (snapshotMetadata, error) {
	var version int
	var document string
	statement := fmt.Sprintf(`SELECT version, document FROM %s LIMIT 1`, quoteIdentifier(metadataTable))
	if err := database.QueryRowContext(ctx, statement).Scan(&version, &document); err != nil {
		return snapshotMetadata{}, fmt.Errorf("read snapshot metadata: %w", err)
	}
	if version > snapshotMetadataVersion {
		return snapshotMetadata{}, fmt.Errorf(
			"snapshot metadata version %d is newer than this build understands (%d)", version, snapshotMetadataVersion)
	}
	var meta snapshotMetadata
	if err := json.Unmarshal([]byte(document), &meta); err != nil {
		return snapshotMetadata{}, fmt.Errorf("decode snapshot metadata: %w", err)
	}
	return meta, nil
}

// metadataOf builds the record for a snapshot. The caller holds whatever lock
// makes item's fields stable.
func metadataOf(item *snapshot) snapshotMetadata {
	meta := snapshotMetadata{
		ID:                  item.id,
		CreatedAt:           item.createdAt,
		Age:                 item.age,
		ConnectionID:        item.connection.ID.String(),
		ConnectionName:      item.connection.Name,
		ConnectionNamespace: item.connection.Namespace,
		BaseProfile:         item.baseProfile,
		Stats:               item.stats,
		Source:              item.source,
		Dest:                item.dest,
		SourceTruncated:     item.sourceCut,
		DestTruncated:       item.destCut,
		Reconcile:           item.reconcile,
		Profiles:            make([]snapshotProfileMetadata, 0, len(item.profiles)),
	}
	if item.connection.ExpiresAt != nil {
		meta.ExpiresAt = *item.connection.ExpiresAt
	}
	for name, materialized := range item.profiles {
		meta.Profiles = append(meta.Profiles, snapshotProfileMetadata{
			Name:    name,
			Table:   materialized.table,
			Columns: materialized.columns,
			Rows:    materialized.rows,
		})
	}
	return meta
}
