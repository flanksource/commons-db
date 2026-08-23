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
//
// The two questions neither of them answers — whether a container is repeated,
// and what a flat_object holds — are read out of the same rows; see
// openSearchShape.
func openSearchBrowserColumns(rows []query.Row, fields []opensearchinspect.Field) []query.ColumnDef {
	byName := make(map[string]opensearchinspect.Field, len(fields))
	flatRoots := make([]string, 0)
	for _, field := range fields {
		byName[field.Name] = field
		if openSearchFieldType(field) == opensearchinspect.ContainerFlatObject {
			flatRoots = append(flatRoots, field.Name)
		}
	}
	shape := openSearchDocumentShape(rows, fields)
	columns := query.InferSampleColumns(rows)
	named := make(map[string]bool, len(columns))
	for i := range columns {
		columns[i].Filter = openSearchColumnFilter(byName, columns[i].Name, shape)
		named[columns[i].Name] = true
	}
	// A flat_object's sub-keys are invisible to the mapping, so the only place
	// they can be offered from is the documents themselves. A document holding a
	// literal dotted key beside the subtree it names would collide with one, and
	// two columns answering to one filter key is a coin toss.
	for _, column := range openSearchFlatObjectColumns(shape, flatRoots) {
		if !named[column.Name] {
			named[column.Name] = true
			columns = append(columns, column)
		}
	}
	return columns
}

// openSearchFlatObjectFilter is what a flat_object sub-key can be narrowed by.
//
// Term-family clauses on one work exactly as they do on a keyword field, so a
// selection pushes down. Two things it must not offer, both measured rather than
// assumed: the terms aggregation over a sub-key returns corrupt buckets, so
// there is no list to pick from; and every value is stored as a keyword whatever
// it was written as, so a range on it compares lexicographically and "1000" sorts
// below "200" — a bound that reads as numeric and is not.
func openSearchFlatObjectFilter() *query.ColumnFilterDef {
	lookup := false
	return &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Lookup: &lookup}
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
	shape openSearchShape,
) *query.ColumnFilterDef {
	disabled := &query.ColumnFilterDef{Disabled: true}
	field, known := fields[name]
	if !known {
		return openSearchFlatObjectSubKeyFilter(fields, name)
	}
	if field.Conflicting || !field.Searchable {
		return disabled
	}
	// The root of a flat_object holds every value in its subtree under one name,
	// so a term on it asks whether any tag has this value — the one question a
	// whole tag map can answer, and the only one, since it has no doc values.
	if openSearchFieldType(field) == opensearchinspect.ContainerFlatObject {
		return openSearchFlatObjectFilter()
	}
	// A leaf of an object the documents repeat cannot be narrowed honestly: the
	// index has no way to say which entry a clause belongs to, so this selection
	// and the next would be matched against different entries of the same
	// document. That is what `nested` exists to fix, and it is a mapping change
	// rather than something a query can work around.
	if field.ContainerType == opensearchinspect.ContainerObject && shape.isRepeated(field.Container) {
		return disabled
	}
	kind, mapped := openSearchFieldFamilies[openSearchFieldType(field)]
	if !mapped {
		return disabled
	}
	nested := ""
	if field.Nested() {
		nested = field.Container
	}
	switch {
	case kind == query.ColumnFilterKindTerms && field.Aggregatable:
		return &query.ColumnFilterDef{Kind: kind, Nested: nested}
	case kind == query.ColumnFilterKindTerms:
		// A value list is the whole point of a keyword field, and this one has no
		// doc values to build it from.
		return disabled
	case kind != query.ColumnFilterKindText:
		// A range, a time bound and a yes/no read the inverted index, which every
		// searchable field has by definition.
		return &query.ColumnFilterDef{Kind: kind, Nested: nested}
	}
	// An analyzed field has no doc values of its own, so it is enumerated through
	// the un-analyzed sibling the mapping keeps beside it — the column keeps its
	// own name while the aggregation and the term clause run on the sibling.
	// Without one there is nothing to enumerate, so it is matched instead.
	if sibling, ok := fields[name+".keyword"]; ok && sibling.Aggregatable {
		return &query.ColumnFilterDef{
			Kind: query.ColumnFilterKindTerms, Field: sibling.Name, Nested: nested,
		}
	}
	return &query.ColumnFilterDef{Kind: query.ColumnFilterKindText, Nested: nested}
}

// openSearchFlatObjectSubKeyFilter resolves a name the mapping does not report.
//
// Under a flat_object root that is a real sub-key: the root's own entry is all
// the mapping has, and asking `_field_caps` for the sub-key by name answers that
// it exists whether or not any document carries it — so the name is taken at
// face value here, exactly as it would be at the index. Anything else is a
// document metadata field or a truncated one, and gets no filter.
func openSearchFlatObjectSubKeyFilter(
	fields map[string]opensearchinspect.Field,
	name string,
) *query.ColumnFilterDef {
	for cut := strings.LastIndex(name, "."); cut > 0; cut = strings.LastIndex(name[:cut], ".") {
		root, known := fields[name[:cut]]
		if !known {
			continue
		}
		if openSearchFieldType(root) == opensearchinspect.ContainerFlatObject {
			return openSearchFlatObjectFilter()
		}
		break
	}
	return &query.ColumnFilterDef{Disabled: true}
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
//
// requested names the columns a selection actually binds to. It is read because
// a flat_object's sub-keys are in no mapping — the console learned them from the
// documents and is naming one back, and refusing it here would withdraw the
// filter it was just offered.
func openSearchFilterColumns(
	fields []opensearchinspect.Field,
	shape openSearchShape,
	requested []string,
) []query.ColumnDef {
	columns := make([]query.ColumnDef, 0, len(fields)+len(requested))
	byName := make(map[string]opensearchinspect.Field, len(fields))
	named := make(map[string]bool, len(fields))
	for _, field := range fields {
		byName[field.Name] = field
	}
	for _, field := range fields {
		columns = append(columns, query.ColumnDef{
			Name: field.Name, Filter: openSearchColumnFilter(byName, field.Name, shape),
		})
		named[field.Name] = true
	}
	for _, name := range requested {
		if named[name] {
			continue
		}
		named[name] = true
		columns = append(columns, query.ColumnDef{
			Name: name, Filter: openSearchColumnFilter(byName, name, shape),
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
	refresh bool,
) ([]opensearchinspect.Field, error) {
	inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{
		CacheKey: searcher.InspectionKey(),
	})
	if err != nil {
		return nil, err
	}
	mappingCtx, cancel := context.WithTimeout(ctx, openSearchMappingTimeout)
	defer cancel()
	catalog, err := inspector.Fields(mappingCtx, opensearchinspect.FieldRequest{
		Target: openSearchFilterTarget(index), Refresh: refresh,
	})
	if err != nil {
		return nil, err
	}
	return catalog.Fields, nil
}
