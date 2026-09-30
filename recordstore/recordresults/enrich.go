// Read-time enrichment: a result type's hook adding computed columns to the
// rows its profile serves, checked to leave the rows it was given intact.
package recordresults

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// Enricher adds a result type's EnrichColumns to rows as they are read: a
// page, a follow or an export. It returns the rows in the order given, one
// for each, with every stored column as it was — cursors and follows rest on
// the rows' seqs and values — and nothing beyond its declared columns added.
type Enricher func(ctx context.Context, rows []query.Row) ([]query.Row, error)

// enrichment is a registered type's enricher and the columns it may add.
type enrichment struct {
	enrich  Enricher
	columns []string
}

// validateEnrichment refuses an enricher without its columns or columns
// without an enricher, and a column that is also one the type stores.
func validateEnrichment(stored []query.ColumnDef, enrich Enricher, columns []query.ColumnDef) error {
	switch {
	case enrich == nil && len(columns) > 0:
		return fmt.Errorf("enrich columns need an Enrich function to fill them")
	case enrich != nil && len(columns) == 0:
		return fmt.Errorf("an Enrich function needs the EnrichColumns it adds")
	}
	for index, column := range columns {
		switch {
		case column.Name == seqColumn || column.Name == streamIDKey ||
			slices.ContainsFunc(stored, func(other query.ColumnDef) bool { return other.Name == column.Name }):
			return fmt.Errorf("enrich column %q is a column the type stores", column.Name)
		case slices.ContainsFunc(columns[:index], func(other query.ColumnDef) bool { return other.Name == column.Name }):
			return fmt.Errorf("enrich column %q is named twice", column.Name)
		}
	}
	return nil
}

// apply runs the enricher over rows and checks what it returned.
func (e enrichment) apply(ctx context.Context, rows []query.Row) ([]query.Row, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	originals := make([]query.Row, len(rows))
	for index, row := range rows {
		originals[index] = maps.Clone(row)
	}
	enriched, err := e.enrich(ctx, rows)
	if err != nil {
		return nil, fmt.Errorf("enrich rows: %w", err)
	}
	if len(enriched) != len(originals) {
		return nil, fmt.Errorf("enrich rows: %d rows came back for %d", len(enriched), len(originals))
	}
	for index, original := range originals {
		for key, value := range original {
			if !reflect.DeepEqual(enriched[index][key], value) {
				return nil, fmt.Errorf("enrich rows: row %d column %q was changed; an enricher only adds its own columns", index, key)
			}
		}
		for key := range enriched[index] {
			if _, stored := original[key]; !stored && !slices.Contains(e.columns, key) {
				return nil, fmt.Errorf("enrich rows: row %d gained column %q, which is not one of the enrich columns", index, key)
			}
		}
	}
	return enriched, nil
}

// enrichmentFor is the enrichment of the result type whose stream req reads,
// if it has one. req names its index streams already: a stream param, or the streams param's
// first, since every stream a read spans holds the same kind.
func (r *Registry) enrichmentFor(ctx dbcontext.Context, req query.ProviderRequest) (enrichment, bool, error) {
	stream, _ := req.Params[streamParam].(string)
	if stream == "" {
		entries, err := requestedStreams(ProviderType, req.Params)
		if err != nil {
			return enrichment{}, false, err
		}
		stream = entries[0]
	}
	meta, err := r.index.Meta(ctx, stream)
	if err != nil {
		return enrichment{}, false, notFound(err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	enriching, ok := r.enrichments[meta.Kind]
	return enriching, ok, nil
}
