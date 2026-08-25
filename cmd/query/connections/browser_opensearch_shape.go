package connections

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

// openSearchShapeSampleSize is how many documents are read to learn the shape
// the mapping does not describe. It is small because the questions it answers
// are about kind, not distribution: whether a container is repeated, and which
// keys a flat_object carries. A field absent from all of them is a field this
// console cannot offer, which is the same answer a larger sample would give for
// anything rare enough to miss here.
const openSearchShapeSampleSize = 20

// openSearchShape is what the documents say and the mapping cannot.
//
// Two questions live here because `_field_caps` has no answer to either. An
// object holding one value and an object holding an array of them are mapped
// identically, so only a document says which — and the difference decides
// whether two selections under it can be correlated at all. A flat_object
// reports no sub-keys whatsoever, and asking for one by name reports it as
// present whether or not any document has it, so only a document says which
// sub-keys exist.
type openSearchShape struct {
	// repeated names the object containers a document holds an array of.
	repeated map[string]bool

	// subKeys lists the sub-keys found under each flat_object root, dotted and
	// sorted.
	subKeys map[string][]string
}

func (s openSearchShape) isRepeated(container string) bool { return s.repeated[container] }

// openSearchDocumentShape reads the shape out of sampled documents.
//
// Only flat_object roots are descended into for sub-keys: every other container
// reports its own leaves through the mapping, which is the better authority
// because it covers the fields this sample happened not to include.
func openSearchDocumentShape(rows []query.Row, fields []opensearchinspect.Field) openSearchShape {
	flatRoots := make(map[string]bool)
	for _, field := range fields {
		if openSearchFieldType(field) == opensearchinspect.ContainerFlatObject {
			flatRoots[field.Name] = true
		}
	}
	shape := openSearchShape{repeated: map[string]bool{}, subKeys: map[string][]string{}}
	found := make(map[string]map[string]bool, len(flatRoots))
	for _, row := range rows {
		for name, value := range row {
			shape.walk(name, value, flatRoots, found)
		}
	}
	for root, keys := range found {
		names := make([]string, 0, len(keys))
		for name := range keys {
			names = append(names, name)
		}
		sort.Strings(names)
		shape.subKeys[root] = names
	}
	return shape
}

func (s openSearchShape) walk(path string, value any, flatRoots map[string]bool, found map[string]map[string]bool) {
	if flatRoots[path] {
		if _, seen := found[path]; !seen {
			found[path] = map[string]bool{}
		}
		collectFlatKeys("", value, found[path])
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			s.walk(path+"."+name, child, flatRoots, found)
		}
	case []any:
		// The path is repeated as soon as one element is an object: everything
		// below it belongs to an entry rather than to the document, so it is not
		// descended into. A leaf under it is reachable only through the entry, and
		// a plain object mapping has no way to say which entry.
		for _, element := range typed {
			if _, object := element.(map[string]any); object {
				s.repeated[path] = true
				return
			}
		}
	}
}

// collectFlatKeys records the dotted keys a flat_object subtree holds. A
// flat_object stores every leaf as a keyword whatever it was written as, so the
// leaf's own type is not recorded — see openSearchFlatObjectColumns for why that
// matters to the filter it gets.
func collectFlatKeys(prefix string, value any, into map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			next := name
			if prefix != "" {
				next = prefix + "." + name
			}
			collectFlatKeys(next, child, into)
		}
	case []any:
		for _, element := range typed {
			collectFlatKeys(prefix, element, into)
		}
	default:
		if prefix != "" {
			into[prefix] = true
		}
	}
}

// openSearchSampleShape reads the shape from the index itself, for the paths
// that need it before they have any rows of their own — resolving a selection,
// and answering what one of its values could be.
//
// It asks match_all rather than the query being browsed: the question is what
// kind of documents this index holds, and scoping it to a filtered result would
// make the answer depend on the selection being resolved against it.
func (h *connectionBrowserHandler) openSearchSampleShape(
	ctx context.Context,
	searcher *opensearch.Searcher,
	index string,
	fields []opensearchinspect.Field,
) (openSearchShape, error) {
	sampleCtx, cancel := context.WithTimeout(ctx, openSearchMappingTimeout)
	defer cancel()
	raw, err := searcher.SearchRaw(h.ctx.Wrap(sampleCtx), opensearch.Request{
		Index: index,
		Query: `{"query":{"match_all":{}}}`,
		Limit: strconv.Itoa(openSearchShapeSampleSize),
	})
	if err != nil {
		return openSearchShape{}, err
	}
	return openSearchDocumentShape(openSearchSourceRows(raw), fields), nil
}

// openSearchSourceRows lifts each hit's document into a row.
func openSearchSourceRows(raw opensearch.Response) []query.Row {
	rows := make([]query.Row, 0, len(raw.Hits.Hits))
	for _, hit := range raw.Hits.Hits {
		row := make(query.Row, len(hit.Source))
		for key, value := range hit.Source {
			row[key] = value
		}
		rows = append(rows, row)
	}
	return rows
}

// openSearchFlatObjectColumns describes the sub-keys of a flat_object as columns
// the console can show and narrow on.
//
// They exist as columns because nothing else in this codebase carries a filter,
// and they cannot come from the mapping: a flat_object reports only its root. A
// sub-key reads through the root it lives under, so the column is a jsonpath over
// it — which is also what infers the backend field, `attrs.app` being both the
// path and the indexed name.
func openSearchFlatObjectColumns(shape openSearchShape, roots []string) []query.ColumnDef {
	sort.Strings(roots)
	columns := make([]query.ColumnDef, 0)
	for _, root := range roots {
		for _, key := range shape.subKeys[root] {
			columns = append(columns, openSearchFlatObjectColumn(root, key))
		}
	}
	return columns
}

func openSearchFlatObjectColumn(root, key string) query.ColumnDef {
	segments := strings.Split(key, ".")
	path := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded, _ := json.Marshal(segment)
		path = append(path, "["+string(encoded)+"]")
	}
	return query.ColumnDef{
		Name:     root + "." + key,
		Source:   root,
		JSONPath: "$" + strings.Join(path, ""),
		Filter:   openSearchFlatObjectFilter(),
	}
}
