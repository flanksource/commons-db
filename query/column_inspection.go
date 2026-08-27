package query

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
)

const maximumInspectedColumns = 100

type ColumnInspectionResult struct {
	Filters map[string]*ColumnFilterDef
	Cache   []inspection.CacheMetadata

	// Counts is the distinct count per inspected *field* that the filter kinds
	// above were chosen from. It is reported rather than kept internal because
	// the count is the whole reason for the choice: without it, "why is this a
	// text box and not a dropdown" can only be answered by re-running the probe
	// by hand.
	Counts map[string]int64
}

type InspectionStatus struct {
	Status  string                     `json:"status"`
	Message string                     `json:"message,omitempty"`
	Cache   []inspection.CacheMetadata `json:"cache,omitempty"`
	Counts  map[string]int64           `json:"counts,omitempty"`
}

type ColumnInspectionProvider interface {
	InspectColumnFilters(context.Context, ProviderRequest, []ColumnDef) (ColumnInspectionResult, error)
}

func inspectColumns(
	ctx context.Context,
	profile Profile,
	request ProviderRequest,
	columns []ColumnDef,
	rawColumns []ColumnDef,
) ([]ColumnDef, *InspectionStatus, error) {
	providerType := request.Provider
	if providerType == "" {
		providerType = profile.Provider.Type
	}
	provider, err := GetProvider(providerType)
	if err != nil {
		return nil, nil, err
	}
	inspector, ok := provider.(ColumnInspectionProvider)
	if !ok {
		return columns, nil, nil
	}

	configured := make(map[string]ColumnDef, len(profile.Columns))
	for _, column := range profile.Columns {
		configured[column.Name] = column
	}
	rawNames := make(map[string]struct{}, len(rawColumns))
	for _, column := range rawColumns {
		rawNames[column.Name] = struct{}{}
	}
	candidates := make([]ColumnDef, 0, len(columns))
	candidateNames := make(map[string]struct{}, len(columns))
	for _, discovered := range columns {
		if discovered.Type != ColumnTypeString && discovered.Type != ColumnTypeStatus && discovered.Type != ColumnTypeHealth {
			continue
		}
		column, exists := configured[discovered.Name]
		if !exists {
			if _, raw := rawNames[discovered.Name]; !raw {
				continue
			}
			candidates = append(candidates, discovered)
			candidateNames[discovered.Name] = struct{}{}
			continue
		}
		if column.Filter != nil && (column.Filter.Disabled || column.Filter.Kind != "") {
			continue
		}
		if (column.CEL != "" || column.JSONPath != "") && (column.Filter == nil || column.Filter.Field == "") {
			continue
		}
		discovered.Source = column.Source
		if column.Filter != nil {
			discovered.Filter = &ColumnFilterDef{Field: column.Filter.Field}
		}
		if _, raw := rawNames[discovered.InspectedField()]; !raw {
			continue
		}
		candidates = append(candidates, discovered)
		candidateNames[discovered.Name] = struct{}{}
	}
	if len(candidates) == 0 {
		return columns, nil, nil
	}
	partialMessage := ""
	if len(candidates) > maximumInspectedColumns {
		partialMessage = fmt.Sprintf("inspection limited to the first %d eligible columns", maximumInspectedColumns)
		candidates = candidates[:maximumInspectedColumns]
		candidateNames = make(map[string]struct{}, len(candidates))
		for _, candidate := range candidates {
			candidateNames[candidate.Name] = struct{}{}
		}
	}

	result, err := inspector.InspectColumnFilters(ctx, request, candidates)
	if err != nil {
		return disableInspectedCandidates(columns, candidateNames), &InspectionStatus{
			Status: "failed", Message: err.Error(),
		}, nil
	}
	inspected := append([]ColumnDef(nil), columns...)
	for index := range inspected {
		if _, candidate := candidateNames[inspected[index].Name]; !candidate {
			continue
		}
		if suggestion := result.Filters[inspected[index].Name]; suggestion != nil {
			copy := *suggestion
			inspected[index].Filter = &copy
		}
	}
	recordCardinalityProbes(ctx, request, providerType, candidates, result)

	status := "complete"
	if partialMessage != "" {
		status = "partial"
	}
	for _, cache := range result.Cache {
		if cache.LastRefreshError != "" {
			status = "partial"
			if partialMessage == "" {
				partialMessage = "using stale inspection metadata after refresh failed"
			}
			break
		}
	}
	return inspected, &InspectionStatus{
		Status: status, Message: partialMessage, Cache: result.Cache, Counts: result.Counts,
	}, nil
}

// InspectedField is the field an inspection probe asks about on behalf of this
// column: the filter's explicit field if it names one, then the source the
// column maps from, then the column's own name.
func (c ColumnDef) InspectedField() string {
	if c.Filter != nil && c.Filter.Field != "" {
		return c.Filter.Field
	}
	if c.Source != "" {
		return c.Source
	}
	return c.Name
}

// recordCardinalityProbes files what the inspection asked and what it decided,
// for a request that asked to be explained.
//
// A column with no suggestion is still recorded: "the count came back under the
// limit, so nothing had to change" is an answer, and its absence would read as
// though the column was never probed at all.
func recordCardinalityProbes(
	ctx context.Context,
	request ProviderRequest,
	provider string,
	candidates []ColumnDef,
	result ColumnInspectionResult,
) {
	recorder := RecorderFrom(ctx)
	if recorder == nil {
		return
	}
	cached := true
	for _, cache := range result.Cache {
		if !cache.Cached {
			cached = false
			break
		}
	}
	for _, column := range candidates {
		field := column.InspectedField()
		probe := CardinalityProbe{
			Provider: provider, Connection: request.Connection,
			Column: column.Name, Distinct: result.Counts[field],
			Limit: DefaultFilterLookupLimit, Cached: cached,
		}
		if field != column.Name {
			probe.Field = field
		}
		if suggestion := result.Filters[column.Name]; suggestion != nil {
			probe.Kind = string(suggestion.Kind)
		}
		recorder.RecordProbe(probe)
	}
}

func disableInspectedCandidates(columns []ColumnDef, candidates map[string]struct{}) []ColumnDef {
	disabled := append([]ColumnDef(nil), columns...)
	for index := range disabled {
		if _, candidate := candidates[disabled[index].Name]; !candidate || disabled[index].Filter != nil {
			continue
		}
		disabled[index].Filter = &ColumnFilterDef{Disabled: true}
	}
	return disabled
}
