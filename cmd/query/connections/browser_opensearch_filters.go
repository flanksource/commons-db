package connections

import (
	"context"
	"strings"
	"time"

	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

// openSearchFieldFamilies groups the mapping types a field can be declared as
// by how a value of it compares. It is the server-side twin of the browser's
// own `fieldFamily`, and the two must agree: the console offers an operator
// from one and the filter compiles through the other.
var openSearchFieldFamilies = map[string]query.ColumnFilterKind{
	"keyword": query.ColumnFilterKindTerms, "constant_keyword": query.ColumnFilterKindTerms,
	"wildcard": query.ColumnFilterKindTerms, "ip": query.ColumnFilterKindTerms,

	"text": query.ColumnFilterKindText, "match_only_text": query.ColumnFilterKindText,
	"search_as_you_type": query.ColumnFilterKindText,

	"date": query.ColumnFilterKindTime, "date_nanos": query.ColumnFilterKindTime,

	"long": query.ColumnFilterKindRange, "integer": query.ColumnFilterKindRange,
	"short": query.ColumnFilterKindRange, "byte": query.ColumnFilterKindRange,
	"double": query.ColumnFilterKindRange, "float": query.ColumnFilterKindRange,
	"half_float": query.ColumnFilterKindRange, "scaled_float": query.ColumnFilterKindRange,
	"unsigned_long": query.ColumnFilterKindRange,

	"boolean": query.ColumnFilterKindBoolean,
}

// openSearchBrowserColumns describes a hit set: the columns the documents
// actually carry, each annotated with how the index can narrow on it.
//
// The display set comes from the rows and the filterability from the mapping,
// because neither answers the other's question. A mapping lists every field the
// index could hold — hundreds on a wide index, most absent from any given hit —
// so a table built from it would be mostly empty columns. A hit says nothing
// about whether a field has doc values, so a filter built from it would offer
// value lists the cluster refuses to produce.
func openSearchBrowserColumns(rows []query.Row, fields []opensearchinspect.Field) []query.ColumnDef {
	byName := make(map[string]opensearchinspect.Field, len(fields))
	for _, field := range fields {
		byName[field.Name] = field
	}
	columns := query.InferSampleColumns(rows)
	for i := range columns {
		columns[i].Filter = openSearchColumnFilter(byName, columns[i].Name)
	}
	return columns
}

// openSearchColumnFilter resolves how one field can be narrowed, or a disabled
// filter when it cannot be.
//
// A field absent from the mapping is a document metadata field (_id, _score) or
// a name the catalog was truncated before reaching; either way nothing here
// knows how it compares. A field whose name means two different types across
// the indices behind one target is refused outright — narrowing on it would
// mean one thing in some documents and another in the rest.
func openSearchColumnFilter(
	fields map[string]opensearchinspect.Field,
	name string,
) *query.ColumnFilterDef {
	disabled := &query.ColumnFilterDef{Disabled: true}
	field, known := fields[name]
	if !known || field.Conflicting || !field.Searchable {
		return disabled
	}
	kind, mapped := openSearchFieldFamilies[openSearchFieldType(field)]
	if !mapped {
		return disabled
	}
	switch {
	case kind == query.ColumnFilterKindTerms && field.Aggregatable:
		return &query.ColumnFilterDef{Kind: kind}
	case kind == query.ColumnFilterKindTerms:
		// A value list is the whole point of a keyword field, and this one has no
		// doc values to build it from.
		return disabled
	case kind != query.ColumnFilterKindText:
		// A range, a time bound and a yes/no read the inverted index, which every
		// searchable field has by definition.
		return &query.ColumnFilterDef{Kind: kind}
	}
	// An analyzed field has no doc values of its own, so it is enumerated through
	// the un-analyzed sibling the mapping keeps beside it — the column keeps its
	// own name while the aggregation and the term clause run on the sibling.
	// Without one there is nothing to enumerate, so it is matched instead.
	if sibling, ok := fields[name+".keyword"]; ok && sibling.Aggregatable {
		return &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Field: sibling.Name}
	}
	return &query.ColumnFilterDef{Kind: query.ColumnFilterKindText}
}

// openSearchFieldType picks the mapping type a field compares as. _field_caps
// reports one type per non-conflicting field; the sort in the inspector makes
// the choice stable for anything that slipped through.
func openSearchFieldType(field opensearchinspect.Field) string {
	if len(field.Types) == 0 {
		return ""
	}
	return field.Types[0]
}

// openSearchFilterTarget names the index a query reads so its mapping can be
// fetched. A wildcard is a legitimate target — `_field_caps` resolves one — and
// has to be declared as such or the inspector refuses it.
func openSearchFilterTarget(index string) opensearchinspect.Target {
	kind := "index"
	if strings.Contains(index, "*") {
		kind = "pattern"
	}
	return opensearchinspect.Target{Name: index, Kind: kind}
}

// openSearchFilterColumns describes every field the index maps, for resolving a
// selection against it.
//
// A filter names a field the index holds, which is not the same set as the
// fields the hits it returned happen to carry: narrowing to the documents that
// have a value nothing on this page has is exactly what a filter is for.
func openSearchFilterColumns(fields []opensearchinspect.Field) []query.ColumnDef {
	columns := make([]query.ColumnDef, 0, len(fields))
	byName := make(map[string]opensearchinspect.Field, len(fields))
	for _, field := range fields {
		byName[field.Name] = field
	}
	for _, field := range fields {
		columns = append(columns, query.ColumnDef{
			Name: field.Name, Filter: openSearchColumnFilter(byName, field.Name),
		})
	}
	return columns
}

// openSearchMappingTimeout bounds the mapping read. It is a metadata request on
// the query path, so a cluster that has stopped answering it must not hold up
// the rows a search would still return.
const openSearchMappingTimeout = 15 * time.Second

// openSearchFieldCatalog reads what an index maps.
//
// It is read on every run rather than echoed back by the console because the
// mapping is the only authority on how a field compares and whether it has the
// doc values a value list needs — facts no hit carries and no client should be
// trusted to restate.
func (h *connectionBrowserHandler) openSearchFieldCatalog(
	ctx context.Context,
	searcher *opensearch.Searcher,
	index string,
) ([]opensearchinspect.Field, error) {
	inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{})
	if err != nil {
		return nil, err
	}
	mappingCtx, cancel := context.WithTimeout(ctx, openSearchMappingTimeout)
	defer cancel()
	catalog, err := inspector.Fields(mappingCtx, openSearchFilterTarget(index))
	if err != nil {
		return nil, err
	}
	return catalog.Fields, nil
}
