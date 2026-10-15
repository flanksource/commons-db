package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

func enrichSQLite(ctx context.Context, db *sql.DB, catalog *Catalog, limits Limits) error {
	budget := limits.withDefaults().MaxColumns
	for schemaIndex := range catalog.Schemas {
		for relationIndex := range catalog.Schemas[schemaIndex].Relations {
			relation := &catalog.Schemas[schemaIndex].Relations[relationIndex]
			if err := sqliteRelation(ctx, db, relation, &budget, limits); err != nil {
				return fmt.Errorf("inspect SQLite relation %q: %w", relation.Name, err)
			}
		}
	}
	if budget < 0 {
		catalog.Truncated = true
		catalog.TruncateReason = "SQLite metadata limit reached"
	}
	return nil
}

func sqliteRelation(ctx context.Context, db *sql.DB, relation *Relation, budget *int, limits Limits) error {
	var definition sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')", relation.Name).Scan(&definition); err != nil {
		return err
	}
	if relation.Type == "view" && definition.Valid {
		if len(definition.String) > limits.withDefaults().MaxDefinitionBytes {
			*budget = -1
		} else {
			relation.ViewDef = definition.String
		}
	}
	if err := sqliteColumns(ctx, db, relation); err != nil {
		return err
	}
	if err := sqliteIndexes(ctx, db, relation, budget); err != nil {
		return err
	}
	if err := sqliteForeignKeys(ctx, db, relation, budget); err != nil {
		return err
	}
	return sqliteTriggers(ctx, db, relation, budget, limits)
}

func sqliteColumns(ctx context.Context, db *sql.DB, relation *Relation) error {
	rows, err := db.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk, cid FROM pragma_table_xinfo(?) ORDER BY cid`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := make([]Column, 0, len(relation.Columns))
	for rows.Next() {
		var name, dataType string
		var notNull, pk, cid int
		var value sql.NullString
		if err := rows.Scan(&name, &dataType, &notNull, &value, &pk, &cid); err != nil {
			return err
		}
		nullable := notNull == 0 && pk == 0
		column := Column{Name: name, DataType: dataType, Ordinal: cid + 1, Nullable: &nullable, PrimaryKey: pk > 0}
		if value.Valid {
			column.Default = &value.String
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	relation.Columns = columns
	return nil
}

func sqliteIndexes(ctx context.Context, db *sql.DB, relation *Relation, budget *int) error {
	rows, err := db.QueryContext(ctx, `SELECT name, "unique", origin FROM pragma_index_list(?) ORDER BY seq`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, origin string
		var unique int
		if err := rows.Scan(&name, &unique, &origin); err != nil {
			return err
		}
		*budget--
		if *budget < 0 {
			continue
		}
		columns, err := sqliteIndexColumns(ctx, db, name)
		if err != nil {
			return err
		}
		relation.Indexes = append(relation.Indexes, Index{Name: name, Unique: unique != 0, Primary: origin == "pk", Columns: columns, Type: "btree"})
		if unique != 0 && len(columns) == 1 {
			for i := range relation.Columns {
				if relation.Columns[i].Name == columns[0] {
					relation.Columns[i].Unique = true
				}
			}
		}
	}
	return rows.Err()
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

func sqliteForeignKeys(ctx context.Context, db *sql.DB, relation *Relation, budget *int) error {
	rows, err := db.QueryContext(ctx, `SELECT id, seq, "table", "from", "to" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	keys := map[int]*ForeignKey{}
	for rows.Next() {
		var id, seq int
		var target, from string
		var to sql.NullString
		if err := rows.Scan(&id, &seq, &target, &from, &to); err != nil {
			return err
		}
		*budget--
		if *budget < 0 {
			continue
		}
		key := keys[id]
		if key == nil {
			key = &ForeignKey{Name: fmt.Sprintf("fk_%s_%d", relation.Name, id), ReferencedTable: target}
			keys[id] = key
		}
		key.Columns = append(key.Columns, from)
		key.ReferencedColumns = append(key.ReferencedColumns, to.String)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ids := make([]int, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		relation.ForeignKeys = append(relation.ForeignKeys, *keys[id])
	}
	return nil
}

func sqliteTriggers(ctx context.Context, db *sql.DB, relation *Relation, budget *int, limits Limits) error {
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`, relation.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return err
		}
		*budget--
		if *budget < 0 {
			continue
		}
		if len(statement) > limits.withDefaults().MaxDefinitionBytes {
			*budget = -1
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
