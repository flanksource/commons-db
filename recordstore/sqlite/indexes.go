// A kind table's indexes: the time index a kind's newest-first pages read, and
// the indexes it declares, recorded in its catalog entry and never dropped.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/recordstore"
)

// indexColumn is one column of an index, by its declared name.
type indexColumn struct {
	Name string `json:"name"`
	Desc bool   `json:"desc,omitempty"`
}

// storedIndex is an index as record_kinds records it: its columns in order.
// A kind table keeps every index any build declared, because migrate/sqlite
// never drops one and a build still reading the file may rely on it.
type storedIndex struct {
	Columns []indexColumn `json:"columns"`
}

// kindIndexes are the indexes schema asks for: its time column's
// (stream, time desc, seq), which serves the newest-first page of a stream,
// then each declared index, led by the stream every read selects.
func kindIndexes(schema recordstore.KindSchema) []storedIndex {
	var indexes []storedIndex
	if schema.Options.TimeColumn != "" {
		indexes = append(indexes, storedIndex{Columns: []indexColumn{
			{Name: streamColumn}, {Name: schema.Options.TimeColumn, Desc: true}, {Name: seqColumn},
		}})
	}
	for _, declared := range schema.Options.Indexes {
		index := storedIndex{Columns: []indexColumn{{Name: streamColumn}}}
		for _, column := range declared.Columns {
			index.Columns = append(index.Columns, indexColumn{Name: column})
		}
		indexes = append(indexes, index)
	}
	return indexes
}

// withIndexes is stored with every index of wanted it lacks, and whether it
// lacked any.
func withIndexes(stored, wanted []storedIndex) ([]storedIndex, bool) {
	all := slices.Clone(stored)
	for _, index := range wanted {
		if !slices.ContainsFunc(all, func(other storedIndex) bool { return slices.Equal(other.Columns, index.Columns) }) {
			all = append(all, index)
		}
	}
	return all, len(all) > len(stored)
}

// name is the index's name on table: the table's, then each column's physical
// name, a descending one marked.
func (i storedIndex) name(table string, physical func(declared string) (string, bool)) (string, error) {
	parts := []string{table, "ix"}
	for _, column := range i.Columns {
		name, ok := physical(column.Name)
		if !ok {
			return "", fmt.Errorf("index of %q names column %q, which its catalog does not record", table, column.Name)
		}
		parts = append(parts, name)
		if column.Desc {
			parts = append(parts, "desc")
		}
	}
	return strings.Join(parts, "_"), nil
}

func decodeIndexes(encoded string) ([]storedIndex, error) {
	var indexes []storedIndex
	if err := json.Unmarshal([]byte(encoded), &indexes); err != nil {
		return nil, fmt.Errorf("decode catalog indexes: %w", err)
	}
	return indexes, nil
}

func encodeIndexes(indexes []storedIndex) (string, error) {
	if indexes == nil {
		indexes = []storedIndex{}
	}
	encoded, err := json.Marshal(indexes)
	return string(encoded), err
}

// kindEntry reads kind's record_kinds entry, reporting false when the file
// has none.
func kindEntry(ctx context.Context, database queryer, kind string) (columns, indexes string, found bool, err error) {
	err = database.QueryRowContext(ctx, `SELECT columns, indexes FROM record_kinds WHERE kind = ?`, kind).Scan(&columns, &indexes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("kind %q: read catalog: %w", kind, err)
	}
	return columns, indexes, true, nil
}
