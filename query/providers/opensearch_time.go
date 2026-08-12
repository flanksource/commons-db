package providers

import (
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query/esdsl"
)

const openSearchTimeMappingTimeout = 15 * time.Second

// ResolveOpenSearchTimeFieldMapping reads the exact field used by active
// time-role parameters. The mapping is runtime metadata and never becomes part
// of the stored profile.
func ResolveOpenSearchTimeFieldMapping(
	ctx dbcontext.Context,
	searcher *opensearch.Searcher,
	index string,
	search esdsl.Search,
	params []esdsl.ParamBinding,
) (*esdsl.TimeFieldMapping, error) {
	if !NeedsOpenSearchTimeFieldMapping(params) {
		return nil, nil
	}
	if strings.TrimSpace(index) == "" {
		return nil, fmt.Errorf("OpenSearch index is required to inspect timeField %q", search.TimeField)
	}
	if strings.TrimSpace(search.TimeField) == "" {
		return nil, fmt.Errorf("a time-from/time-to parameter requires timeField on the search specification")
	}
	now := time.Now().UTC()
	inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{})
	if err != nil {
		return nil, err
	}
	mappingCtx, cancel := ctx.WithTimeout(openSearchTimeMappingTimeout)
	defer cancel()
	catalog, err := inspector.Fields(mappingCtx, opensearchinspect.FieldRequest{
		Target: openSearchTimeTarget(index),
		Names:  []string{search.TimeField},
	})
	if err != nil {
		return nil, fmt.Errorf("inspect OpenSearch timeField %q on %q: %w", search.TimeField, index, err)
	}
	if len(catalog.Fields) != 1 || catalog.Fields[0].Name != search.TimeField {
		return nil, fmt.Errorf("OpenSearch timeField %q is not mapped on %q", search.TimeField, index)
	}
	field := catalog.Fields[0]
	if field.Conflicting || len(field.Types) != 1 {
		return nil, fmt.Errorf("OpenSearch timeField %q has conflicting mapping types %v on %q", search.TimeField, field.Types, index)
	}
	return &esdsl.TimeFieldMapping{Type: field.Types[0], Now: now}, nil
}

// NeedsOpenSearchTimeFieldMapping reports whether resolved parameters contain
// a non-empty time bound.
func NeedsOpenSearchTimeFieldMapping(params []esdsl.ParamBinding) bool {
	for _, param := range params {
		if param.Role != esdsl.RoleTimeFrom && param.Role != esdsl.RoleTimeTo {
			continue
		}
		switch value := param.Value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(value) == "" {
				continue
			}
		case []string:
			if len(value) == 0 {
				continue
			}
		case []any:
			if len(value) == 0 {
				continue
			}
		}
		return true
	}
	return false
}

func openSearchTimeTarget(index string) opensearchinspect.Target {
	kind := "index"
	if strings.Contains(index, "*") {
		kind = "pattern"
	}
	return opensearchinspect.Target{Name: index, Kind: kind}
}
