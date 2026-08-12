package snapshots

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
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
		m.mu.RUnlock()
		return m.descriptor(item, existing), nil
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
	if err := writeTable(ctx, item.db, table, columns, rows); err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, err
	}
	profile := snapshotProfile(profileName, table, columns, item.connection.Name, len(rows))
	created := materialization{profile: profile, table: table, columns: columns, rows: len(rows)}
	m.mu.Lock()
	item.profiles[profileName] = created
	m.profiles[profileName] = item.id
	m.mu.Unlock()
	return m.descriptor(item, created), nil
}

func snapshotProfile(name, table string, columns []query.ColumnDef, connectionName string, rows int) query.Profile {
	limits := &query.RowLimits{MaxExportRows: max(rows, query.DefaultMaxExportRows)}
	profileColumns := slices.Clone(columns)
	for index := range profileColumns {
		if isStructured(profileColumns[index].Type) {
			profileColumns[index].Source = profileColumns[index].Name
			profileColumns[index].JSONPath = "$"
		}
	}
	return query.Profile{
		Name: name, Virtual: true, ReadOnly: true,
		Provider: query.ProviderConfig{Type: "sql", Connection: "connection://reconciliations/" + connectionName},
		Query:    selectTable(table, columns), Columns: profileColumns,
		Order: query.Order{{Column: "row_id", Unique: true}}, Limits: limits,
		Output: []string{"table", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf"},
	}
}

func selectTable(table string, columns []query.ColumnDef) string {
	selects := make([]string, len(columns))
	for index, column := range columns {
		selects[index] = fmt.Sprintf(`"c%d" AS %s`, index, quoteIdentifier(column.Name))
	}
	return fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selects, ", "), quoteIdentifier(table))
}

func writeTable(ctx context.Context, database *sql.DB, table string, columns []query.ColumnDef, rows []query.Row) error {
	definitions := make([]string, len(columns))
	for index, column := range columns {
		definitions[index] = fmt.Sprintf(`"c%d" %s`, index, sqliteType(column.Type))
	}
	if _, err := database.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (%s)`, quoteIdentifier(table), strings.Join(definitions, ", "))); err != nil {
		return fmt.Errorf("create table %q: %w", table, err)
	}
	if index := slices.IndexFunc(columns, func(column query.ColumnDef) bool { return column.Name == "row_id" }); index >= 0 {
		statement := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s ("c%d")`,
			quoteIdentifier(table+"_row_id"), quoteIdentifier(table), index)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("index table %q: %w", table, err)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	markers := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement, err := database.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s VALUES (%s)`, quoteIdentifier(table), markers))
	if err != nil {
		return fmt.Errorf("prepare table %q: %w", table, err)
	}
	defer statement.Close()
	for rowIndex, row := range rows {
		values := make([]any, len(columns))
		for columnIndex, column := range columns {
			values[columnIndex], err = sqliteValue(row[column.Name])
			if err != nil {
				return fmt.Errorf("row %d column %q: %w", rowIndex, column.Name, err)
			}
		}
		if _, err := statement.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("insert row %d into %q: %w", rowIndex, table, err)
		}
	}
	return nil
}

func readRows(ctx context.Context, database *sql.DB, table string, columns []query.ColumnDef) ([]query.Row, error) {
	rows, err := database.QueryContext(ctx, selectTable(table, columns)+` ORDER BY "row_id"`)
	if err != nil {
		return nil, fmt.Errorf("read snapshot profile: %w", err)
	}
	defer rows.Close()
	result, err := db.ScanRows[query.Row](rows)
	if err != nil {
		return nil, err
	}
	for _, row := range result {
		for _, column := range columns {
			if !isStructured(column.Type) {
				continue
			}
			encoded, ok := row[column.Name].(string)
			if !ok || encoded == "" {
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
				return nil, fmt.Errorf("decode structured column %q: %w", column.Name, err)
			}
			row[column.Name] = decoded
		}
	}
	return result, nil
}

func isStructured(columnType query.ColumnType) bool {
	return columnType == query.ColumnTypeJSON || columnType == query.ColumnTypeKeyValue || columnType == query.ColumnTypeKeyValues
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

func sqliteValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string, []byte:
		return typed, nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	case *time.Time:
		if typed == nil {
			return nil, nil
		}
		return typed.UTC().Format(time.RFC3339Nano), nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(encoded), nil
	}
}

func sqliteType(columnType query.ColumnType) string {
	switch columnType {
	case query.ColumnTypeNumber, query.ColumnTypeDuration, query.ColumnTypeBytes:
		return "NUMERIC"
	case query.ColumnTypeBoolean:
		return "INTEGER"
	default:
		return "TEXT"
	}
}

func materializationFingerprint(options profiles.ReconcileMaterializeOptions) string {
	hash := sha256.Sum256([]byte(options.Profile + "\x00" + options.CEL + "\x00" + strings.Join(options.Columns, "\x00")))
	return hex.EncodeToString(hash[:])
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
