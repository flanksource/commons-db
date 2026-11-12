package query

import (
	"encoding/json"
	"fmt"
	"github.com/flanksource/commons-db/context"
)

func normalizeTraceRow(ctx context.Context, profile Profile, row Row) error {
	if profile.RenderMode() != RenderTraces && profile.Search == nil {
		return nil
	}
	if err := profile.Spans.Validate(); err != nil {
		return err
	}
	canonical, err := mappedSpanDocument(ctx, profile, row)
	if err != nil {
		return err
	}
	for key, value := range canonical.Row() {
		row[key] = value
	}
	row["_trace"] = canonical
	return nil
}

type MappingPreview struct {
	NormalizedRows   []Row           `json:"normalizedRows"`
	AvailableColumns []ColumnDef     `json:"availableColumns"`
	Rows             []Row           `json:"rows"`
	ResultColumns    []ResultColumn  `json:"resultColumns"`
	Document         json.RawMessage `json:"document"`
}

// PreviewMapping evaluates an authoring draft against retained sample input without querying a provider.
func PreviewMapping(ctx context.Context, profile Profile, rows []Row) (*MappingPreview, error) {
	if len(rows) > DefaultMaxPageSize {
		return nil, fmt.Errorf("mapping preview exceeds %d rows", DefaultMaxPageSize)
	}
	input, err := copyMappingRows(rows)
	if err != nil {
		return nil, err
	}
	normalized, err := copyMappingRows(input)
	if err != nil {
		return nil, err
	}
	for index, row := range normalized {
		if err := normalizeTraceRow(ctx, profile, row); err != nil {
			return nil, fmt.Errorf("row %d: %w", index+1, err)
		}
	}
	mapped, _, err := applyRowTransforms(ctx, profile, input)
	if err != nil {
		return nil, err
	}
	columns, err := DescribeResultColumns(ResultColumnOptions{Profile: profile})
	if err != nil {
		return nil, err
	}
	document, err := RenderClickyPage(profile, mapped)
	if err != nil {
		return nil, err
	}
	return &MappingPreview{NormalizedRows: normalized, AvailableColumns: SpanColumns(normalized), Rows: mapped, ResultColumns: columns, Document: json.RawMessage(document)}, nil
}
