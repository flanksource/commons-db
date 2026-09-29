// Kind tables for a batch: a kind the batch declares itself is reconciled
// additively with the file, and any other kind is resolved as for an append.
package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/recordstore"
)

// invalidEntry marks an error a batch entry caused by what it asked for, as
// opposed to one the file raised while applying it; the entry fails alone.
type invalidEntry struct{ error }

func (e invalidEntry) Unwrap() error { return e.error }

// batchTable is one kind's table for a batch, or the error that fails every
// entry appending to it.
type batchTable struct {
	table kindTable
	err   error
}

// batchTables resolves every kind the batch declares or appends to, before its
// transaction begins, since creating or widening a table takes the writer. A
// kind the batch's own schemas declare gets a table built from that schema,
// widened in the file with the columns it adds but never cached, so the
// backend's own schema resolver is left as it was. An error that is neither
// the kind's nor its schema's fails the whole batch.
func (b *Backend) batchTables(ctx context.Context, batch recordstore.Batch) (map[string]batchTable, error) {
	declared := make(map[string]recordstore.KindSchema, len(batch.Schemas))
	for _, schema := range batch.Schemas {
		if _, duplicate := declared[schema.Kind]; duplicate {
			return nil, fmt.Errorf("batch %q declares kind %q twice", batch.ID, schema.Kind)
		}
		declared[schema.Kind] = schema
	}
	kinds := make([]string, 0, len(batch.Schemas)+len(batch.Entries))
	for _, schema := range batch.Schemas {
		kinds = append(kinds, schema.Kind)
	}
	for _, entry := range batch.Entries {
		if entry.Op == recordstore.BatchAppend && recordstore.ValidateKind(entry.Kind) == nil {
			kinds = append(kinds, entry.Kind)
		}
	}
	tables := map[string]batchTable{}
	for _, kind := range kinds {
		if _, resolved := tables[kind]; resolved {
			continue
		}
		schema, foreign := declared[kind]
		table, err := b.batchTable(ctx, kind, schema, foreign)
		if err != nil && !errors.As(err, new(invalidEntry)) && !errors.Is(err, recordstore.ErrSchemaConflict) {
			return nil, err
		}
		tables[kind] = batchTable{table: table, err: err}
	}
	return tables, nil
}

func (b *Backend) batchTable(ctx context.Context, kind string, schema recordstore.KindSchema, foreign bool) (kindTable, error) {
	if !foreign {
		var err error
		if schema, err = recordstore.ResolveKind(b.schema, kind); err != nil {
			return kindTable{}, invalidEntry{fmt.Errorf("kind %q: %w", kind, err)}
		}
	} else if err := schema.Validate(); err != nil {
		return kindTable{}, invalidEntry{err}
	}
	table, err := newKindTable(schema)
	if err != nil {
		return kindTable{}, invalidEntry{err}
	}
	if !foreign {
		return b.kindTable(kind)
	}
	return b.reconcileTable(ctx, kind, table)
}
