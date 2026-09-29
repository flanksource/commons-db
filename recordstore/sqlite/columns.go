package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/db/sqlitetable"
	sqlitemigrate "github.com/flanksource/commons-db/migrate/sqlite"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// storedColumn is one column entry of a kind's catalog, "declared=physical:SQLTYPE:type":
// a physical column of the kind table and the declared column it stores.
type storedColumn struct {
	declared, physical string

	// storage is the entry after its names, as columnStorage renders it.
	storage string
}

// storedCatalog is a kind's record_kinds entry: one column entry for every
// physical column of its table, in the table's order, then the key a keyed
// kind holds once per stream and, for a kind that replaces stored rows, its
// conflict policy. A table only ever gains columns, so the entry may describe
// columns the current declaration no longer names — another build sharing the
// file still reads and writes them.
type storedCatalog struct {
	columns    []storedColumn
	key        string
	onConflict recordstore.OnConflict

	// indexes are the table's indexes, which record_kinds keeps beside the
	// column entries.
	indexes []storedIndex
}

// replaceEntry is the entry recording that a kind replaces stored rows. A kind
// that skips them records none, so its entry reads as version 5 wrote it.
const replaceEntry = "onConflict:replace"

// parseStoredColumn splits an entry from the right: a physical name is a safe
// identifier and the storage part holds exactly two colons, so the declared
// name before them may contain anything.
func parseStoredColumn(entry string) (storedColumn, bool) {
	typeAt := strings.LastIndex(entry, ":")
	if typeAt < 0 {
		return storedColumn{}, false
	}
	storageAt := strings.LastIndex(entry[:typeAt], ":")
	if storageAt < 0 {
		return storedColumn{}, false
	}
	namesAt := strings.LastIndex(entry[:storageAt], "=")
	if namesAt < 0 {
		return storedColumn{}, false
	}
	return storedColumn{declared: entry[:namesAt], physical: entry[namesAt+1 : storageAt], storage: entry[storageAt:]}, true
}

// compressed reports an entry stored as a compressed blob.
func (c storedColumn) compressed() bool {
	return strings.HasPrefix(c.storage, ":"+compressedStorage+":")
}

// def is the declared column the entry stores, with the type it recorded.
func (c storedColumn) def() query.ColumnDef {
	return query.ColumnDef{Name: c.declared, Type: query.ColumnType(c.storage[strings.LastIndex(c.storage, ":")+1:])}
}

// readCatalog reads a kind's catalog entry — its columns and its indexes —
// against its table, and describes, as a mismatch, an entry that does not name
// the table's columns in order.
func readCatalog(ctx context.Context, tx *sql.Tx, table, stored, storedIndexes string) (storedCatalog, string, error) {
	var parts []string
	if err := json.Unmarshal([]byte(stored), &parts); err != nil {
		return storedCatalog{}, "", fmt.Errorf("decode catalog columns: %w", err)
	}
	indexes, err := decodeIndexes(storedIndexes)
	if err != nil {
		return storedCatalog{}, "", err
	}
	physical, err := sqlitetable.PhysicalColumns(ctx, tx, table)
	if err != nil {
		return storedCatalog{}, "", err
	}
	mismatch := fmt.Sprintf("catalog columns %s naming a table whose columns are %q", stored, physical)
	if len(parts) < len(physical) {
		return storedCatalog{}, mismatch, nil
	}
	catalog := storedCatalog{indexes: indexes}
	for _, part := range parts[len(physical):] {
		switch {
		case strings.HasPrefix(part, "key:") && catalog.key == "" && catalog.onConflict == recordstore.OnConflictSkip:
			catalog.key = strings.TrimPrefix(part, "key:")
		case part == replaceEntry && catalog.key != "" && catalog.onConflict == recordstore.OnConflictSkip:
			catalog.onConflict = recordstore.OnConflictReplace
		default:
			return storedCatalog{}, mismatch, nil
		}
	}
	parts = parts[:len(physical)]
	for index, part := range parts {
		column, ok := parseStoredColumn(part)
		if !ok || column.physical != physical[index] {
			return storedCatalog{}, mismatch, nil
		}
		if slices.ContainsFunc(catalog.columns, func(other storedColumn) bool { return other.declared == column.declared }) {
			return storedCatalog{}, fmt.Sprintf("catalog columns %s naming column %q twice", stored, column.declared), nil
		}
		catalog.columns = append(catalog.columns, column)
	}
	return catalog, "", nil
}

// catalogOf is the catalog of a table just created, whose physical columns are
// its declared ones in order, and whose indexes are the ones its kind asks for.
func catalogOf(table kindTable) storedCatalog {
	catalog := storedCatalog{key: table.schema.Options.Key, onConflict: table.schema.Options.OnConflict, indexes: kindIndexes(table.schema)}
	for index, column := range table.Columns {
		catalog.columns = append(catalog.columns, storedColumn{declared: column.Name, physical: table.StoredAs[index], storage: table.storage(column)})
	}
	return catalog
}

func (c storedCatalog) encode() (string, error) {
	parts := make([]string, len(c.columns), len(c.columns)+2)
	for index, column := range c.columns {
		parts[index] = column.declared + "=" + column.physical + column.storage
	}
	if c.key != "" {
		parts = append(parts, "key:"+c.key)
	}
	if c.onConflict == recordstore.OnConflictReplace {
		parts = append(parts, replaceEntry)
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}

// adopt gives table the physical name the catalog recorded for each of its
// declared columns, and returns the declared columns the table does not have.
// A declared column recorded with other storage, a different key or a
// different conflict policy is a mismatch: the rows already written would
// mean something else.
func (table *kindTable) adopt(catalog storedCatalog) ([]query.ColumnDef, string) {
	if catalog.key != table.schema.Options.Key {
		return nil, fmt.Sprintf("key %q, now %q", catalog.key, table.schema.Options.Key)
	}
	if catalog.onConflict != table.schema.Options.OnConflict {
		return nil, fmt.Sprintf("conflict policy %s, now %s", catalog.onConflict, table.schema.Options.OnConflict)
	}
	var added []query.ColumnDef
	table.StoredAs = make([]string, len(table.Columns))
	for index, column := range table.Columns {
		at := slices.IndexFunc(catalog.columns, func(stored storedColumn) bool { return stored.declared == column.Name })
		if at < 0 {
			added = append(added, column)
			continue
		}
		if stored, storage := catalog.columns[at].storage, table.storage(column); stored != storage {
			return nil, fmt.Sprintf("column %q stored as %s, now %s", column.Name, strings.TrimPrefix(stored, ":"), strings.TrimPrefix(storage, ":"))
		}
		table.StoredAs[index] = catalog.columns[at].physical
	}
	return added, ""
}

// widen adds to table the declared columns it lacks, under physical names
// derived around every column the table already has, and the indexes its kind
// asks for that it lacks. migrate/sqlite reconciles them against the table
// the whole catalog declares, so a table that has drifted from its catalog in
// any other way is refused, not patched. Both are recorded in the kind's
// catalog.
func widen(ctx context.Context, tx *sql.Tx, kind string, table *kindTable, catalog storedCatalog, added []query.ColumnDef, indexes []storedIndex) error {
	taken := make([]string, len(catalog.columns))
	for index, column := range catalog.columns {
		taken[index] = column.physical
	}
	names := make([]string, len(added))
	for index, column := range added {
		names[index] = column.Name
	}
	physical, err := sqlitetable.PhysicalNames(names, taken, sqlitetable.Bare(ctx, tx))
	if err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	for index, column := range added {
		catalog.columns = append(catalog.columns, storedColumn{declared: column.Name, physical: physical[index], storage: table.storage(column)})
		table.StoredAs[slices.IndexFunc(table.Columns, func(declared query.ColumnDef) bool { return declared.Name == column.Name })] = physical[index]
	}
	catalog.indexes = indexes
	if err := catalog.reconcile(ctx, tx, kind, table.Name); err != nil {
		return err
	}
	entries, err := catalog.encode()
	if err != nil {
		return err
	}
	recorded, err := encodeIndexes(catalog.indexes)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE record_kinds SET columns = ?, indexes = ? WHERE kind = ?`, entries, recorded, kind); err != nil {
		return fmt.Errorf("kind %q: record added columns and indexes: %w", kind, err)
	}
	return nil
}

// reconcile brings the table name up to the catalog: its recorded columns,
// its key and every recorded index. Building an index blocks the writer for
// as long as it takes, so the time is logged.
func (c storedCatalog) reconcile(ctx context.Context, tx *sql.Tx, kind, name string) error {
	declared, err := c.declare(name)
	if err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	started := time.Now()
	if err := sqlitemigrate.ReconcileTables(ctx, tx, sqlitemigrate.ReconcileOptions{}, declared); err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	if len(c.indexes) > 0 {
		logger.V(1).Infof("sqlite record store: kind %q table and %d indexes reconciled in %s", kind, len(c.indexes), time.Since(started))
	}
	return nil
}

// declare is the kind table the catalog describes, as createKindTable creates
// it: every recorded column, the store's primary key, every recorded index —
// which migrate/sqlite would otherwise try to drop — and a keyed kind's unique
// index.
func (c storedCatalog) declare(name string) (*schema.Table, error) {
	table := sqlitetable.Table{Name: name, PrimaryKey: []string{streamColumn, seqColumn}}
	for _, column := range c.columns {
		table.Columns = append(table.Columns, column.def())
		table.StoredAs = append(table.StoredAs, column.physical)
		if column.compressed() {
			table.Compressed = append(table.Compressed, column.declared)
		}
	}
	declared, err := table.Declare()
	if err != nil {
		return nil, err
	}
	at := func(column string) (int, bool) {
		index := slices.IndexFunc(c.columns, func(stored storedColumn) bool { return stored.declared == column })
		return index, index >= 0
	}
	for _, index := range c.indexes {
		indexName, err := index.name(name, func(column string) (string, bool) {
			position, ok := at(column)
			if !ok {
				return "", false
			}
			return c.columns[position].physical, true
		})
		if err != nil {
			return nil, err
		}
		built := schema.NewIndex(indexName)
		for _, column := range index.Columns {
			position, _ := at(column.Name)
			built.AddParts(schema.NewColumnPart(declared.Columns[position]).SetDesc(column.Desc))
		}
		declared.AddIndexes(built)
	}
	if c.key == "" {
		return declared, nil
	}
	var parts []*schema.Column
	for _, key := range []string{streamColumn, c.key} {
		at := slices.IndexFunc(c.columns, func(column storedColumn) bool { return column.declared == key })
		if at < 0 {
			return nil, fmt.Errorf("table %q keys on %q, which its catalog does not record", name, key)
		}
		parts = append(parts, declared.Columns[at])
	}
	return declared.AddIndexes(schema.NewUniqueIndex(name + "_key").AddColumns(parts...)), nil
}
