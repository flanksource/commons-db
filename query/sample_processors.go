package query

import (
	"fmt"
	"maps"

	"github.com/flanksource/commons-db/context"
)

func previewSampleProcessors(ctx context.Context, profile Profile, input []Row) (*ProcessorPreview, []Row, error) {
	preview := &ProcessorPreview{Input: cloneSampleRows(input), Stages: []ProcessorPreviewStage{}}
	result := &Result{Profile: profile.Name, Rows: cloneSampleRows(input)}
	for index, spec := range profile.Processors {
		resolved, err := spec.Resolve()
		if err != nil {
			return nil, nil, fmt.Errorf("processor %d: %w", index, err)
		}
		registered, err := GetProcessor(resolved.Type)
		if err != nil {
			return nil, nil, err
		}
		rowsIn := len(result.Rows)
		result, err = registered.Process(ctx, resolved, result)
		if err != nil {
			return nil, nil, fmt.Errorf("processor %q: %w", resolved.Label(), err)
		}
		if result == nil {
			return nil, nil, fmt.Errorf("processor %q returned a nil result", resolved.Label())
		}
		preview.Stages = append(preview.Stages, ProcessorPreviewStage{
			Index: index, Label: resolved.Label(), Type: resolved.Type,
			RowsIn: rowsIn, RowsOut: len(result.Rows), Rows: cloneSampleRows(result.Rows),
		})
	}
	return preview, cloneSampleRows(result.Rows), nil
}

func cloneSampleRows(rows []Row) []Row {
	cloned := make([]Row, len(rows))
	for index, row := range rows {
		cloned[index] = maps.Clone(row)
	}
	return cloned
}
