package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type sqliteEnrichment struct {
	ctx          context.Context
	db           *sql.DB
	catalog      *Catalog
	limits       Limits
	columnsLeft  int
	metadataLeft int
}

func enrichSQLite(ctx context.Context, db *sql.DB, catalog *Catalog, limits Limits) error {
	limits = limits.withDefaults()
	inspector := sqliteEnrichment{ctx: ctx, db: db, catalog: catalog, limits: limits, columnsLeft: limits.MaxColumns, metadataLeft: limits.MaxMetadata}
	for _, schema := range catalog.Schemas {
		for _, relation := range schema.Relations {
			inspector.columnsLeft -= len(relation.Columns)
		}
	}
	for schemaIndex := range catalog.Schemas {
		for relationIndex := range catalog.Schemas[schemaIndex].Relations {
			relation := &catalog.Schemas[schemaIndex].Relations[relationIndex]
			if err := inspector.relation(relation); err != nil {
				return fmt.Errorf("inspect SQLite relation %q: %w", relation.Name, err)
			}
		}
	}
	return nil
}

func (inspector *sqliteEnrichment) truncate(reason string) {
	if !inspector.catalog.Truncated {
		inspector.catalog.Truncated = true
		inspector.catalog.TruncateReason = reason
	}
}

func (inspector *sqliteEnrichment) takeMetadata() bool {
	if inspector.metadataLeft == 0 {
		inspector.truncate("SQLite metadata limit reached")
		return false
	}
	inspector.metadataLeft--
	return true
}

func (inspector *sqliteEnrichment) relation(relation *Relation) error {
	var definition sql.NullString
	if err := inspector.db.QueryRowContext(inspector.ctx, "SELECT sql FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')", relation.Name).Scan(&definition); err != nil {
		return err
	}
	if relation.Type == "view" && definition.Valid {
		if len(definition.String) > inspector.limits.MaxDefinitionBytes {
			inspector.truncate("SQLite definition limit reached")
		} else {
			relation.ViewDef = definition.String
		}
	}
	if err := inspector.columns(relation); err != nil {
		return err
	}
	if err := inspector.indexes(relation); err != nil {
		return err
	}
	if err := inspector.foreignKeys(relation); err != nil {
		return err
	}
	return inspector.triggers(relation)
}

func (inspector *sqliteEnrichment) columns(relation *Relation) error {
	var withoutRowID, strict bool
	if err := inspector.db.QueryRowContext(inspector.ctx, `SELECT wr, strict FROM pragma_table_list WHERE schema = 'main' AND name = ?`, relation.Name).Scan(&withoutRowID, &strict); err != nil {
		return err
	}
	rows, err := inspector.db.QueryContext(inspector.ctx, `SELECT name, type, "notnull", dflt_value, pk, cid, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	type columnInfo struct {
		column              Column
		notNull, pk, hidden int
	}
	var found []columnInfo
	pkCount := 0
	for rows.Next() {
		var entry columnInfo
		var cid int
		var value sql.NullString
		if err := rows.Scan(&entry.column.Name, &entry.column.DataType, &entry.notNull, &value, &entry.pk, &cid, &entry.hidden); err != nil {
			return err
		}
		entry.column.Ordinal = cid + 1
		entry.column.PrimaryKey = entry.pk > 0
		if value.Valid {
			entry.column.Default = &value.String
		}
		if entry.pk > 0 {
			pkCount++
		}
		found = append(found, entry)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	retained := make(map[string]int, len(relation.Columns))
	for index, column := range relation.Columns {
		retained[column.Name] = index
	}
	for _, entry := range found {
		nonNullable := entry.notNull != 0 || entry.pk > 0 && (withoutRowID || strict || pkCount == 1 && strings.EqualFold(entry.column.DataType, "INTEGER"))
		nullable := !nonNullable
		entry.column.Nullable = &nullable
		if index, ok := retained[entry.column.Name]; ok {
			relation.Columns[index] = entry.column
		} else if entry.hidden != 0 {
			if inspector.columnsLeft == 0 {
				inspector.truncate("column limit reached")
				continue
			}
			relation.Columns = append(relation.Columns, entry.column)
			inspector.columnsLeft--
		}
	}
	return nil
}

func (inspector *sqliteEnrichment) indexes(relation *Relation) error {
	rows, err := inspector.db.QueryContext(inspector.ctx, `SELECT name, "unique", origin, partial FROM pragma_index_list(?) ORDER BY seq`, relation.Name)
	if err != nil {
		return err
	}
	type indexInfo struct {
		name, origin    string
		unique, partial bool
	}
	var found []indexInfo
	for rows.Next() {
		var entry indexInfo
		if err := rows.Scan(&entry.name, &entry.unique, &entry.origin, &entry.partial); err != nil {
			rows.Close()
			return err
		}
		found = append(found, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entry := range found {
		if !inspector.takeMetadata() {
			continue
		}
		columns, err := sqliteIndexColumns(inspector.ctx, inspector.db, entry.name)
		if err != nil {
			return err
		}
		index := Index{Name: entry.name, Unique: entry.unique, Primary: entry.origin == "pk", Columns: columns, Type: "btree"}
		if entry.partial {
			var definition string
			if err := inspector.db.QueryRowContext(inspector.ctx, `SELECT sql FROM sqlite_master WHERE name = ? AND type = 'index'`, entry.name).Scan(&definition); err != nil {
				return err
			}
			index.Filter = sqliteIndexFilter(definition)
			if index.Filter == "" {
				return fmt.Errorf("partial SQLite index %q has no WHERE predicate", entry.name)
			}
		}
		relation.Indexes = append(relation.Indexes, index)
		if entry.unique && !entry.partial && len(columns) == 1 {
			for i := range relation.Columns {
				if relation.Columns[i].Name == columns[0] {
					relation.Columns[i].Unique = true
				}
			}
		}
	}
	return nil
}

func sqliteIndexFilter(definition string) string {
	quoted, depth := byte(0), 0
	for index := 0; index < len(definition); index++ {
		current := definition[index]
		if quoted != 0 {
			if current == quoted {
				if index+1 < len(definition) && definition[index+1] == quoted {
					index++
				} else {
					quoted = 0
				}
			}
			continue
		}
		switch current {
		case '\'', '"', '`':
			quoted = current
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.EqualFold(definition[index:min(index+5, len(definition))], "WHERE") {
				return strings.TrimSpace(definition[index+5:])
			}
		}
	}
	return ""
}

func sqliteIndexColumns(ctx context.Context, db *sql.DB, name string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column sql.NullString
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		if column.Valid {
			columns = append(columns, column.String)
		}
	}
	return columns, rows.Err()
}

type sqliteForeignKeyRow struct {
	id, seq      int
	target, from string
	to           sql.NullString
}

func (inspector *sqliteEnrichment) foreignKeys(relation *Relation) error {
	rows, err := inspector.db.QueryContext(inspector.ctx, `SELECT id, seq, "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, relation.Name)
	if err != nil {
		return err
	}
	var found []sqliteForeignKeyRow
	for rows.Next() {
		var entry sqliteForeignKeyRow
		if err := rows.Scan(&entry.id, &entry.seq, &entry.target, &entry.from, &entry.to); err != nil {
			rows.Close()
			return err
		}
		found = append(found, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	primaryColumns := map[string][]string{}
	for offset := 0; offset < len(found); {
		end := offset + 1
		for end < len(found) && found[end].id == found[offset].id {
			end++
		}
		if inspector.takeMetadata() {
			key := ForeignKey{Name: fmt.Sprintf("fk_%s_%d", relation.Name, found[offset].id), ReferencedTable: found[offset].target}
			for _, entry := range found[offset:end] {
				key.Columns = append(key.Columns, entry.from)
				to := entry.to.String
				if !entry.to.Valid {
					if _, ok := primaryColumns[entry.target]; !ok {
						primaryColumns[entry.target], err = sqlitePrimaryColumns(inspector.ctx, inspector.db, entry.target)
						if err != nil {
							return err
						}
					}
					if entry.seq >= len(primaryColumns[entry.target]) {
						return fmt.Errorf("referenced table %q has no primary-key column at position %d", entry.target, entry.seq)
					}
					to = primaryColumns[entry.target][entry.seq]
				}
				key.ReferencedColumns = append(key.ReferencedColumns, to)
			}
			relation.ForeignKeys = append(relation.ForeignKeys, key)
		}
		offset = end
	}
	return nil
}

func sqlitePrimaryColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func (inspector *sqliteEnrichment) triggers(relation *Relation) error {
	rows, err := inspector.db.QueryContext(inspector.ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return err
		}
		if !inspector.takeMetadata() {
			continue
		}
		if len(statement) > inspector.limits.MaxDefinitionBytes {
			inspector.truncate("SQLite definition limit reached")
			continue
		}
		upper := strings.ToUpper(statement)
		trigger := Trigger{Name: name, SQL: statement}
		for _, timing := range []string{"BEFORE", "AFTER", "INSTEAD OF"} {
			if strings.Contains(upper, timing) {
				trigger.Timing = timing
				break
			}
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if strings.Contains(upper, event) {
				trigger.Event = event
				break
			}
		}
		relation.Triggers = append(relation.Triggers, trigger)
	}
	return rows.Err()
}
