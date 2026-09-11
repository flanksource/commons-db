package snapshots

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

func (m *Manager) Materialize(ctx context.Context, options profiles.ReconcileMaterializeOptions) (profiles.ReconcileSnapshotDescriptor, error) {
	item, release, err := m.acquireSnapshot(options.SnapshotID)
	if err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, err
	}
	defer release()
	item.materializeMu.Lock()
	defer item.materializeMu.Unlock()
	m.mu.RLock()
	base, found := item.profiles[options.Profile]
	m.mu.RUnlock()
	if !found {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("snapshot profile %q not found", options.Profile)
	}
	if strings.TrimSpace(options.CEL) == "" && len(options.Columns) == 0 {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("materialization requires cel or columns")
	}
	fingerprint := materializationFingerprint(options)
	profileName := strings.TrimSuffix(options.Profile, "/") + "/materialized-" + fingerprint[:12]
	m.mu.RLock()
	if existing, ok := item.profiles[profileName]; ok {
		descriptor := m.descriptorLocked(item, existing)
		m.mu.RUnlock()
		return descriptor, nil
	}
	m.mu.RUnlock()

	rows, err := readRows(ctx, item.db, base.table, base.columns)
	if err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, err
	}
	columns := slices.Clone(base.columns)
	if strings.TrimSpace(options.CEL) != "" {
		expression, err := query.CompileRowExpr(dbcontext.NewContext(ctx), options.CEL)
		if err != nil {
			return profiles.ReconcileSnapshotDescriptor{}, err
		}
		rows, err = expression.Rows(map[string]any{
			"rows": rows, "stats": item.stats, "source": item.source, "dest": item.dest,
		})
		if err != nil {
			return profiles.ReconcileSnapshotDescriptor{}, err
		}
		if len(rows) == 0 {
			return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("CEL materialization returned no rows, so its columns cannot be inferred")
		}
		columns = query.InferSampleColumns(rows)
	}
	if len(options.Columns) > 0 {
		columns, rows, err = projectRows(columns, rows, options.Columns)
		if err != nil {
			return profiles.ReconcileSnapshotDescriptor{}, err
		}
	}
	for index := range rows {
		rows[index]["row_id"] = index + 1
	}
	columns = append(columns, query.ColumnDef{Name: "row_id", Type: query.ColumnTypeNumber, Hidden: true})
	table := "materialized_" + fingerprint
	if err := sqlitetable.Write(ctx, item.db, snapshotTable(table, columns), rows); err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, err
	}
	profile := snapshotProfile(profileName, table, columns, item.connection.Name, len(rows))
	created := materialization{profile: profile, table: table, columns: columns, rows: len(rows)}

	// Persist before registering. A crash between the two leaves a durable
	// record for a table that exists, and the reload picks it up; the reverse
	// would lose a materialization the caller already holds a URL for.
	//
	// The record is snapshotted under the read lock and written without it:
	// m.mu is process-wide and this is blocking file I/O. That is only safe
	// because item.materializeMu — held for this whole function — serializes
	// every whole-record rewrite for this snapshot.
	m.mu.RLock()
	meta := metadataOf(item)
	m.mu.RUnlock()
	meta.Profiles = append(meta.Profiles, snapshotProfileMetadata{
		Name: profileName, Table: table, Columns: columns, Rows: len(rows),
	})
	if err := writeSnapshotMetadata(ctx, item.db, meta); err != nil {
		_, _ = item.db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, sqlitetable.QuoteIdentifier(table)))
		return profiles.ReconcileSnapshotDescriptor{}, err
	}

	m.mu.Lock()
	item.profiles[profileName] = created
	m.profiles[profileName] = item.id
	descriptor := m.descriptorLocked(item, created)
	m.mu.Unlock()
	return descriptor, nil
}

func snapshotProfile(name, table string, columns []query.ColumnDef, connectionName string, rows int) query.Profile {
	limits := &query.RowLimits{MaxExportRows: max(rows, query.DefaultMaxExportRows)}
	return query.Profile{
		Name: name, Virtual: true, ReadOnly: true,
		Provider: query.ProviderConfig{Type: "sql", Connection: "connection://reconciliations/" + connectionName},
		Query:    snapshotTable(table, columns).Select(), Columns: sqlitetable.ProfileColumns(columns),
		Order: query.Order{{Column: "row_id", Unique: true}}, Limits: limits,
		Output: []string{"table", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf"},
	}
}

// snapshotTable is a snapshot's table: its row_id, when it has one, is the
// unique order every snapshot profile pages by.
func snapshotTable(name string, columns []query.ColumnDef) sqlitetable.Table {
	table := sqlitetable.Table{Name: name, Columns: columns}
	if slices.ContainsFunc(columns, func(column query.ColumnDef) bool { return column.Name == "row_id" }) {
		table.Unique = []string{"row_id"}
	}
	return table
}

func readRows(ctx context.Context, database *sql.DB, table string, columns []query.ColumnDef) ([]query.Row, error) {
	rows, err := database.QueryContext(ctx, snapshotTable(table, columns).Select()+` ORDER BY "row_id"`)
	if err != nil {
		return nil, fmt.Errorf("read snapshot profile: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result, err := db.ScanRows[query.Row](rows)
	if err != nil {
		return nil, err
	}
	for _, row := range result {
		if err := sqlitetable.DecodeStructured(columns, row); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func projectRows(columns []query.ColumnDef, rows []query.Row, selected []string) ([]query.ColumnDef, []query.Row, error) {
	seen := map[string]bool{}
	byName := map[string]query.ColumnDef{}
	for _, column := range columns {
		byName[column.Name] = column
	}
	projectedColumns := make([]query.ColumnDef, 0, len(selected))
	for _, name := range selected {
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("export column cannot be empty")
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("export column %q is duplicated", name)
		}
		column, found := byName[name]
		if !found || column.Hidden {
			return nil, nil, fmt.Errorf("export column %q does not exist", name)
		}
		seen[name] = true
		projectedColumns = append(projectedColumns, column)
	}
	if len(projectedColumns) == 0 {
		return nil, nil, fmt.Errorf("at least one export column is required")
	}
	projectedRows := make([]query.Row, len(rows))
	for index, row := range rows {
		projectedRows[index] = query.Row{}
		for _, column := range projectedColumns {
			projectedRows[index][column.Name] = row[column.Name]
		}
	}
	return projectedColumns, projectedRows, nil
}

func materializationFingerprint(options profiles.ReconcileMaterializeOptions) string {
	hash := sha256.Sum256([]byte(options.Profile + "\x00" + options.CEL + "\x00" + strings.Join(options.Columns, "\x00")))
	return hex.EncodeToString(hash[:])
}
