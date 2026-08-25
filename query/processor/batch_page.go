package processor

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/fxamacker/cbor/v2"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type batchPageState struct {
	Pending []query.Row `cbor:"pending"`
}

var batchStateEncoder = func() cbor.EncMode {
	options := cbor.CanonicalEncOptions()
	options.Time = cbor.TimeRFC3339Nano
	options.TimeTag = cbor.EncTagRequired
	mode, err := options.EncMode()
	if err != nil {
		panic(fmt.Sprintf("create batch state encoder: %v", err))
	}
	return mode
}()

var batchStateDecoder = func() cbor.DecMode {
	mode, err := (cbor.DecOptions{
		TimeTag:        cbor.DecTagRequired,
		DefaultMapType: reflect.TypeFor[map[string]any](),
	}).DecMode()
	if err != nil {
		panic(fmt.Sprintf("create batch state decoder: %v", err))
	}
	return mode
}()

func (batchProcessor) ProcessPage(
	ctx context.Context,
	spec query.ProcessorSpec,
	page query.Page,
	state []byte,
) (query.Page, []byte, error) {
	cfg, err := query.DecodeOptions[BatchConfig](spec.Config)
	if err != nil {
		return query.Page{}, nil, err
	}
	pending, err := decodeBatchPageState(state)
	if err != nil {
		return query.Page{}, nil, err
	}
	ordered := slices.Clone(page.Rows)
	if cfg.Order == OrderDescending {
		slices.Reverse(ordered)
		ordered = append(ordered, pending...)
	} else {
		ordered = append(pending, ordered...)
	}
	rows, next, err := processBatchPage(ctx, cfg, ordered, page.HasMore)
	if err != nil {
		return query.Page{}, nil, err
	}
	if cfg.Order == OrderDescending {
		slices.Reverse(rows)
	}
	page.Rows, page.Styles = rows, nil
	encoded, err := encodeBatchPageState(next)
	if err != nil {
		return query.Page{}, nil, err
	}
	return page, encoded, nil
}

func processBatchPage(
	ctx context.Context,
	cfg BatchConfig,
	ordered []query.Row,
	hasMore bool,
) ([]query.Row, []query.Row, error) {
	if len(ordered) == 0 {
		return nil, nil, nil
	}
	compiled, err := compileBatch(ctx, cfg, ordered)
	if err != nil {
		return nil, nil, err
	}
	groups, err := compiled.group(ordered)
	if err != nil {
		return nil, nil, err
	}
	var pending []query.Row
	if hasMore {
		if cfg.Order == OrderDescending {
			if len(groups[0]) < cfg.batchLimit() {
				pending, groups = groups[0], groups[1:]
			}
		} else {
			last := len(groups) - 1
			if len(groups[last]) < cfg.batchLimit() {
				pending, groups = groups[last], groups[:last]
			}
		}
	}
	rows, err := collapseBatchGroups(compiled, groups)
	return rows, pending, err
}

func collapseBatchGroups(compiled *compiledBatch, groups [][]query.Row) ([]query.Row, error) {
	rows := make([]query.Row, 0, len(groups))
	for index, group := range groups {
		transformed, err := compiled.collapse(group)
		if err != nil {
			return nil, fmt.Errorf("batch %d (%d rows): %w", index, len(group), err)
		}
		rows = append(rows, transformed...)
	}
	return rows, nil
}

func encodeBatchPageState(rows []query.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	encoded, err := batchStateEncoder.Marshal(batchPageState{Pending: rows})
	if err != nil {
		return nil, fmt.Errorf("encode batch state: %w", err)
	}
	return encoded, nil
}

func decodeBatchPageState(state []byte) ([]query.Row, error) {
	if len(state) == 0 {
		return nil, nil
	}
	var decoded batchPageState
	if err := batchStateDecoder.Unmarshal(state, &decoded); err != nil {
		return nil, fmt.Errorf("decode batch state: %w", err)
	}
	if len(decoded.Pending) == 0 {
		return nil, fmt.Errorf("decode batch state: pending batch is empty")
	}
	return decoded.Pending, nil
}
