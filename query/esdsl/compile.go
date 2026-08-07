package esdsl

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CompileRequest is the input to Compile.
type CompileRequest struct {
	// Search is the specification to compile.
	Search Search

	// Params are the resolved profile parameters, with their roles.
	Params []ParamBinding

	// Referenced names parameters already consumed outside the specification —
	// a parameter the engine interpolated into the provider options, say. They
	// count toward the all-parameters-referenced check without appearing as an
	// operand of their own.
	Referenced []string

	// PageSize is how many hits the caller asked this page for. Zero leaves the
	// specification's own size in place.
	PageSize int
}

// Compiled is a search request ready to send.
type Compiled struct {
	// Body is the Query DSL request body. It never contains size: the searcher
	// sends size as a URL parameter, so a body size would be overridden.
	Body map[string]any

	// Size is the resolved hit cap. Zero means unspecified.
	Size int

	// From is the resolved offset. Zero means no offset.
	From int

	// Capped reports that the specification's own size held the page below the
	// PageSize asked for, so a short page is not read as the end of the index.
	Capped bool

	// ParamUses reports each condition field that structurally consumed a
	// parameter. Providers use it to avoid applying the same include twice when
	// a parameter also has a native include/exclude field binding.
	ParamUses []ParamUse
}

// ParamUse is one structural parameter operand and its condition field.
type ParamUse struct {
	Name  string
	Field string
}

// JSON encodes the request body.
func (c Compiled) JSON() (string, error) {
	encoded, err := json.Marshal(c.Body)
	if err != nil {
		return "", fmt.Errorf("encode compiled search: %w", err)
	}
	return string(encoded), nil
}

// PrettyJSON encodes the request body for display.
func (c Compiled) PrettyJSON() (string, error) {
	encoded, err := json.MarshalIndent(c.Body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode compiled search: %w", err)
	}
	return string(encoded), nil
}

// Compile validates the specification, binds its parameters, and renders the
// OpenSearch request body.
func Compile(req CompileRequest) (Compiled, error) {
	if err := req.Search.Validate(); err != nil {
		return Compiled{}, err
	}
	root, size, from, paramUses, err := bindSearch(req.Search, req.Params, req.Referenced)
	if err != nil {
		return Compiled{}, err
	}
	// The page a caller asked for wins over a specification that asks for more,
	// and a specification asking for less is honoured but reported: a page cut
	// short by the profile's own size is otherwise indistinguishable from the
	// end of the index.
	var capped bool
	if req.PageSize > 0 {
		switch {
		case size == 0 || size > req.PageSize:
			size = req.PageSize
		case size < req.PageSize:
			capped = true
		}
	}

	query, err := compileRoot(root)
	if err != nil {
		return Compiled{}, err
	}
	body := map[string]any{"query": query}
	if err := addOutput(body, req.Search); err != nil {
		return Compiled{}, err
	}
	if from > 0 {
		body["from"] = from
	}
	return Compiled{Body: body, Size: size, From: from, Capped: capped, ParamUses: paramUses}, nil
}

func compileRoot(root *bound) (map[string]any, error) {
	if root == nil {
		return map[string]any{"match_all": map[string]any{}}, nil
	}
	return compileNode(*root, "query")
}

func compileNode(node bound, path string) (map[string]any, error) {
	switch node.spec.Op {
	case OpBool:
		return compileBool(node, path)
	case OpNested:
		inner, err := compileBool(node, path)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"path": node.spec.Path, "query": inner}
		addString(body, "score_mode", node.spec.ScoreMode)
		addBoost(body, node.spec.Boost)
		return map[string]any{"nested": body}, nil
	default:
		return compileLeaf(node, path)
	}
}

func compileBool(node bound, path string) (map[string]any, error) {
	buckets := map[Occur][]any{}
	for i, child := range node.children {
		clause, err := compileNode(child, fmt.Sprintf("%s.conditions[%d]", path, i))
		if err != nil {
			return nil, err
		}
		occur := child.spec.Occur.normalized()
		buckets[occur] = append(buckets[occur], clause)
	}
	body := map[string]any{}
	for _, occur := range Occurs() {
		if len(buckets[occur]) > 0 {
			body[string(occur)] = buckets[occur]
		}
	}
	if len(body) == 0 {
		return map[string]any{"match_all": map[string]any{}}, nil
	}
	if len(buckets[OccurShould]) > 0 {
		if node.spec.MinimumShouldMatch != "" {
			body["minimum_should_match"] = node.spec.MinimumShouldMatch
		} else {
			body["minimum_should_match"] = 1
		}
	}
	addBoost(body, node.spec.Boost)
	return map[string]any{"bool": body}, nil
}

// addOutput renders the response-shaping half of the specification: sort,
// _source, requested fields, hit counting and aggregations.
func addOutput(body map[string]any, search Search) error {
	if len(search.Sort) > 0 {
		sorts := make([]any, 0, len(search.Sort))
		for _, sort := range search.Sort {
			sorts = append(sorts, sort.compile())
		}
		body["sort"] = sorts
	}
	if source := compileSource(search.Source); source != nil {
		body["_source"] = source
	}
	if len(search.StoredFields) > 0 {
		body["stored_fields"] = append([]string{}, search.StoredFields...)
	}
	if len(search.Fields) > 0 {
		body["fields"] = append([]string{}, search.Fields...)
	}
	if track := search.TrackTotalHits.value(); track != nil {
		body["track_total_hits"] = track
	}
	if len(search.Aggregations) > 0 {
		aggregations := make(map[string]any, len(search.Aggregations))
		for name, raw := range search.Aggregations {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return fmt.Errorf("aggregation %q: %w", name, err)
			}
			aggregations[name] = decoded
		}
		body["aggs"] = aggregations
	}
	return nil
}

func (s SortBy) compile() any {
	body := map[string]any{}
	if order := strings.ToLower(s.Order); order != "" {
		body["order"] = order
	}
	addString(body, "mode", s.Mode)
	addString(body, "missing", s.Missing)
	addString(body, "unmapped_type", s.UnmappedType)
	if len(body) == 0 {
		return s.Field
	}
	return map[string]any{s.Field: body}
}

func compileSource(source *Source) any {
	if source == nil {
		return nil
	}
	if source.Enabled != nil && !*source.Enabled {
		return false
	}
	body := map[string]any{}
	if len(source.Includes) > 0 {
		body["includes"] = append([]string{}, source.Includes...)
	}
	if len(source.Excludes) > 0 {
		body["excludes"] = append([]string{}, source.Excludes...)
	}
	if len(body) > 0 {
		return body
	}
	if source.Enabled != nil {
		return true
	}
	return nil
}
