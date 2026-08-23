package providers

import (
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

const openSearchTimeMappingTimeout = 15 * time.Second

// ResolveOpenSearchTimeFieldMapping reads the exact field used by active
// time-role parameters. The mapping is runtime metadata and never becomes part
// of the stored profile.
type OpenSearchTimeFieldMappingRequest struct {
	Searcher   *opensearch.Searcher
	Index      string
	Search     esdsl.Search
	Params     []esdsl.ParamBinding
	Inspection query.InspectionOptions
}

func ResolveOpenSearchTimeFieldMapping(ctx dbcontext.Context, request OpenSearchTimeFieldMappingRequest) (*esdsl.TimeFieldMapping, error) {
	if !NeedsOpenSearchTimeFieldMapping(request.Params) {
		return nil, nil
	}
	if request.Searcher == nil {
		return nil, fmt.Errorf("OpenSearch searcher is required to inspect timeField %q", request.Search.TimeField)
	}
	if strings.TrimSpace(request.Index) == "" {
		return nil, fmt.Errorf("OpenSearch index is required to inspect timeField %q", request.Search.TimeField)
	}
	if strings.TrimSpace(request.Search.TimeField) == "" {
		return nil, fmt.Errorf("a time-from/time-to parameter requires timeField on the search specification")
	}
	now := time.Now().UTC()
	inspector, err := opensearchinspect.New(request.Searcher.GetRawClient(), opensearchinspect.Options{
		CacheKey: request.Searcher.InspectionKey(),
	})
	if err != nil {
		return nil, err
	}
	mappingCtx, cancel := ctx.WithTimeout(openSearchTimeMappingTimeout)
	defer cancel()
	catalog, err := inspector.Fields(mappingCtx, opensearchinspect.FieldRequest{
		Target:         openSearchTimeTarget(request.Index),
		Names:          []string{request.Search.TimeField},
		IncludeFormats: true,
		Refresh:        request.Inspection.Refresh,
	})
	if err != nil {
		return nil, fmt.Errorf("inspect OpenSearch timeField %q on %q: %w", request.Search.TimeField, request.Index, err)
	}
	if len(catalog.Fields) != 1 || catalog.Fields[0].Name != request.Search.TimeField {
		return nil, fmt.Errorf("OpenSearch timeField %q is not mapped on %q", request.Search.TimeField, request.Index)
	}
	field := catalog.Fields[0]
	if field.Conflicting || len(field.Types) != 1 {
		return nil, fmt.Errorf("OpenSearch timeField %q has conflicting mapping types %v on %q", request.Search.TimeField, field.Types, request.Index)
	}
	if field.FormatConflicting {
		return nil, fmt.Errorf("OpenSearch timeField %q has conflicting date formats on %q", request.Search.TimeField, request.Index)
	}
	return &esdsl.TimeFieldMapping{Type: field.Types[0], Format: field.Format, Now: now}, nil
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
