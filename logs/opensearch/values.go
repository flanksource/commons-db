package opensearch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const (
	valuesAggregation = "__clicky_values"
	totalAggregation  = "__clicky_total"
	// scopedAggregation is the one child every scoping wrapper puts its real
	// aggregation under, so the response is read back by walking a chain rather
	// than by rebuilding the shape that was sent.
	scopedAggregation = "__clicky_scope"
	// DefaultValueLimit caps how many distinct values one lookup returns when the
	// caller does not say.
	DefaultValueLimit = 1000
)

// ValuesRequest asks what a field holds. Body scopes the question to the
// documents a query already narrows to; nil asks the whole index.
type ValuesRequest struct {
	Index string
	Field string
	// Search keeps only the values containing it, matched as a substring.
	Search string
	Limit  int
	Body   map[string]any

	// Nested names the `nested` field Field lives inside, and Where pins the
	// entry of it the values are read from — the key of a key/value tag list.
	//
	// Both are required for a nested field to answer at all: its entries are
	// indexed as separate documents, so an aggregation that does not descend into
	// them returns no buckets, and one that descends without pinning the entry
	// returns every tag's values mixed together.
	Nested string
	Where  map[string]string
}

// Value is one distinct value and how many documents carry it.
type Value struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// ValuesResult carries the values the terms aggregation returned and how many
// distinct ones exist behind them, which is what tells an author the list is a
// window rather than the whole set.
type ValuesResult struct {
	Values []Value `json:"values"`
	Total  int     `json:"total"`
}

// DistinctValues answers what values a field holds, ordered by document count.
// The request body is reused as the aggregation's scope, so the values reflect
// whatever the query already narrows to.
func (t *Searcher) DistinctValues(ctx context.Context, req ValuesRequest) (ValuesResult, error) {
	if req.Field == "" {
		return ValuesResult{}, ctx.Oops().Errorf("field is empty")
	}
	if len(req.Where) > 0 && req.Nested == "" {
		return ValuesResult{}, ctx.Oops().Errorf(
			"value lookup for %q pins an entry without naming the nested field it belongs to", req.Field)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultValueLimit
	}
	search := strings.TrimSpace(req.Search)
	terms := map[string]any{"field": req.Field, "size": limit}
	if search != "" {
		terms["include"] = ContainsRegex(search)
	}

	body := req.Body
	if body == nil {
		body = map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
	}
	body["size"] = 0
	// A lookup counts distinct values, so it replaces whatever aggregations the
	// query carries. Both spellings are cleared: OpenSearch rejects a body that
	// declares aggs and aggregations together.
	delete(body, "aggs")
	body["aggregations"] = map[string]any{
		valuesAggregation: scopeAggregation(req, map[string]any{"terms": terms}),
		totalAggregation:  scopeAggregation(req, totalValuesAggregation(req.Field, search)),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ValuesResult{}, fmt.Errorf("encode OpenSearch value lookup: %w", err)
	}
	response, err := t.SearchRaw(ctx, Request{Index: req.Index, Query: string(encoded), Limit: "0"})
	if err != nil {
		return ValuesResult{}, err
	}
	return parseValueAggregations(response.Aggregations, search != "")
}

// scopeAggregation descends an aggregation into the entry of a nested field the
// values are read from.
//
// Both halves are needed and neither is optional. Without the nested step the
// aggregation runs over parent documents, where the field does not exist, and
// returns nothing; without the entry filter it runs over every entry, and a
// lookup for the values of one tag key answers with the values of all of them.
func scopeAggregation(req ValuesRequest, inner map[string]any) map[string]any {
	if req.Nested == "" {
		return inner
	}
	if len(req.Where) > 0 {
		inner = map[string]any{
			"filter":       map[string]any{"bool": map[string]any{"filter": pinnedEntryClauses(req.Where)}},
			"aggregations": map[string]any{scopedAggregation: inner},
		}
	}
	return map[string]any{
		"nested":       map[string]any{"path": req.Nested},
		"aggregations": map[string]any{scopedAggregation: inner},
	}
}

// pinnedEntryClauses renders the constants that pick the entry, in field order
// so a body is byte-stable across runs.
func pinnedEntryClauses(where map[string]string) []any {
	fields := make([]string, 0, len(where))
	for field := range where {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	clauses := make([]any, 0, len(fields))
	for _, field := range fields {
		clauses = append(clauses, map[string]any{"term": map[string]any{field: where[field]}})
	}
	return clauses
}

func totalValuesAggregation(field, search string) map[string]any {
	cardinality := map[string]any{"cardinality": map[string]any{"field": field}}
	if search == "" {
		return cardinality
	}
	return map[string]any{
		"filter":       map[string]any{"regexp": map[string]any{field: ContainsRegex(search)}},
		"aggregations": map[string]any{"values": cardinality},
	}
}

// ContainsRegex renders a substring match as the anchored regexp OpenSearch
// aggregations take, escaping every character its regexp grammar reserves.
func ContainsRegex(search string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `.`, `\.`, `?`, `\?`, `+`, `\+`, `*`, `\*`, `|`, `\|`,
		`{`, `\{`, `}`, `\}`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
		`"`, `\"`, `@`, `\@`, `#`, `\#`, `&`, `\&`, `<`, `\<`, `>`, `\>`, `~`, `\~`,
	)
	return ".*" + replacer.Replace(search) + ".*"
}

// unwrapAggregation walks past the scoping wrappers a request added — a nested
// step, an entry filter, a search filter — to the aggregation that carries key.
//
// Walking rather than unwrapping a known depth is what keeps the reader honest
// about a request it did not build: the wrappers are the caller's choice, and a
// reader that assumed one shape would misread a response for another.
func unwrapAggregation(node map[string]any, key string) (any, bool) {
	if value, ok := node[key]; ok {
		return value, true
	}
	children := make([]string, 0, len(node))
	for name := range node {
		if _, nested := node[name].(map[string]any); nested {
			children = append(children, name)
		}
	}
	sort.Strings(children)
	for _, name := range children {
		if value, ok := unwrapAggregation(node[name].(map[string]any), key); ok {
			return value, true
		}
	}
	return nil, false
}

func parseValueAggregations(aggregations map[string]any, searched bool) (ValuesResult, error) {
	values, ok := aggregations[valuesAggregation].(map[string]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup response is missing %q aggregation", valuesAggregation)
	}
	raw, found := unwrapAggregation(values, "buckets")
	if !found {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup aggregation %q has no buckets", valuesAggregation)
	}
	buckets, ok := raw.([]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup aggregation %q has invalid buckets", valuesAggregation)
	}
	result := ValuesResult{Values: make([]Value, 0, len(buckets))}
	for _, raw := range buckets {
		bucket, ok := raw.(map[string]any)
		if !ok || bucket["key"] == nil {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup returned an invalid bucket")
		}
		count, ok := numberToInt64(bucket["doc_count"])
		if !ok {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup bucket %v has invalid doc_count", bucket["key"])
		}
		result.Values = append(result.Values, Value{Value: fmt.Sprint(bucket["key"]), Count: count})
	}
	total, ok := aggregations[totalAggregation].(map[string]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup response is missing %q aggregation", totalAggregation)
	}
	if searched {
		// The search filter names its cardinality "values", so the walk stops at
		// the wrapper rather than at the count inside it.
		scoped, found := unwrapAggregation(total, "values")
		if !found {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has invalid values aggregation")
		}
		if total, ok = scoped.(map[string]any); !ok {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has invalid values aggregation")
		}
	}
	counted, found := unwrapAggregation(total, "value")
	if !found {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has no value")
	}
	count, ok := numberToInt64(counted)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has invalid value")
	}
	result.Total = int(count)
	return result, nil
}

func numberToInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
