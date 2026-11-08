// Package metrics stores wide, numeric time series using the record store's
// dynamic schemas. Every metric is a column; identities are separate dimensions.
package metrics

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

type Definition struct {
	Name    string `json:"name"`
	Unit    string `json:"unit,omitempty"`
	Counter bool   `json:"counter,omitempty"`
}

type TableOptions struct {
	Kind       string
	Metrics    []Definition
	MaxMetrics int
}
type Sample struct {
	Entity      string
	At          time.Time
	Tick        int64
	Values      map[string]any
	Definitions []Definition
}

type DynamicMetricTable struct {
	mu          sync.Mutex
	kind        string
	limit       int
	definitions map[string]Definition
}

func NewDynamicMetricTable(options TableOptions) (*DynamicMetricTable, error) {
	if err := recordstore.ValidateKind(options.Kind); err != nil {
		return nil, err
	}
	if options.MaxMetrics < 0 {
		return nil, fmt.Errorf("max metrics must be positive")
	}
	limit := options.MaxMetrics
	if limit == 0 {
		limit = 256
	}
	t := &DynamicMetricTable{kind: options.Kind, limit: limit, definitions: map[string]Definition{}}
	for _, definition := range options.Metrics {
		if err := addDefinition(t.definitions, definition); err != nil {
			return nil, err
		}
	}
	if len(t.definitions) > limit {
		return nil, fmt.Errorf("metric definitions: %w", recordstore.ErrCapacity)
	}
	return t, nil
}

func addDefinition(definitions map[string]Definition, definition Definition) error {
	if definition.Name == "" || slices.Contains([]string{"entity", "observedAt", "tick", "stream_id", "seq"}, definition.Name) {
		return fmt.Errorf("invalid metric name %q", definition.Name)
	}
	if previous, ok := definitions[definition.Name]; ok && previous != definition {
		return fmt.Errorf("metric %s metadata changed", definition.Name)
	}
	definitions[definition.Name] = definition
	return nil
}

func (t *DynamicMetricTable) Row(sample Sample) (recordstore.Row, error) {
	if sample.Entity == "" || sample.At.IsZero() || sample.Tick <= 0 {
		return nil, fmt.Errorf("metric sample requires entity, observation time and positive tick")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	definitions := maps.Clone(t.definitions)
	for _, definition := range sample.Definitions {
		if err := addDefinition(definitions, definition); err != nil {
			return nil, err
		}
	}
	row := recordstore.Row{"entity": sample.Entity, "observedAt": sample.At.UTC(), "tick": sample.Tick}
	for name, value := range sample.Values {
		if _, ok := definitions[name]; !ok {
			if err := addDefinition(definitions, Definition{Name: name}); err != nil {
				return nil, err
			}
		}
		if err := numeric(value); err != nil {
			return nil, fmt.Errorf("metric %s: %w", name, err)
		}
		row[name] = value
	}
	if len(definitions) > t.limit {
		return nil, fmt.Errorf("metrics exceed %d columns: %w", t.limit, recordstore.ErrCapacity)
	}
	t.definitions = definitions
	return row, nil
}

func numeric(value any) error {
	if value == nil {
		return nil
	}
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Float64()
		if err != nil {
			return fmt.Errorf("invalid metric number: %w", err)
		}
		value = parsed
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	case reflect.Float32, reflect.Float64:
		if !math.IsNaN(v.Float()) && !math.IsInf(v.Float(), 0) {
			return nil
		}
	}
	return fmt.Errorf("expected a finite number or null, got %T", value)
}

func (t *DynamicMetricTable) Definitions() []Definition {
	t.mu.Lock()
	defer t.mu.Unlock()
	definitions := slices.Collect(maps.Values(t.definitions))
	slices.SortFunc(definitions, func(a, b Definition) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return definitions
}

func (t *DynamicMetricTable) Schema() recordstore.KindSchema {
	columns := []query.ColumnDef{{Name: "entity", Type: query.ColumnTypeString}, {Name: "observedAt", Type: query.ColumnTypeDateTime}, {Name: "tick", Type: query.ColumnTypeNumber}}
	for _, definition := range t.Definitions() {
		columns = append(columns, query.ColumnDef{Name: definition.Name, Type: query.ColumnTypeNumber, Unit: definition.Unit})
	}
	return recordstore.KindSchema{Kind: t.kind, Columns: columns, Options: recordstore.KindOptions{Dynamic: true, MaxDynamicColumns: t.limit, TimeColumn: "observedAt", Indexes: []recordstore.IndexDef{{Columns: []string{"entity", "observedAt"}}}}}
}
