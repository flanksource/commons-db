package processor

import (
	"fmt"
	"slices"
	"sort"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

// Batch grouping and transform defaults.
const (
	// DefaultBatchMax caps how many adjacent rows one batch may absorb, so a
	// runaway continuation predicate folds a page of logs, not a million rows.
	DefaultBatchMax = 1000

	// OrderAscending means the rows arrive oldest first and are batched as they
	// come.
	OrderAscending = "asc"

	// OrderDescending means the rows arrive newest first — the normal shape of a
	// log query. Batches are formed in reverse so a merged message reads
	// chronologically, and the output keeps the input's ordering.
	OrderDescending = "desc"

	// KeepFirst builds the merged row from the earliest row in the batch.
	KeepFirst = "first"

	// KeepLast builds it from the latest.
	KeepLast = "last"
)

// timestampColumns are probed, in order, when BatchConfig.Column is unset.
var timestampColumns = []string{"timestamp", "@timestamp", "time", "firstObserved", "startTime"}

// BatchConfig declares how adjacent rows are grouped into batches and what the
// batch collapses to. It is the configuration of the "cel.batch" processor.
//
// Grouping is sequential — runs of adjacent rows — not a hash group-by. That is
// what a multiline merge needs (a stack frame belongs to the line above it, not
// to every line that shares its timestamp), it is O(n), and it stays incremental
// so the same batcher can later drive a streaming session.
type BatchConfig struct {
	// Partition never merges rows whose values differ here, whatever the
	// timestamps say. Naming a column no row has is not an error: every row
	// reads empty, so the partition simply never splits.
	Partition []string `json:"partition,omitempty" yaml:"partition,omitempty"`

	// Column is the timestamp read by the default grouping rule. When empty the
	// first of timestamp/@timestamp/time/firstObserved/startTime present on the
	// first row is used.
	Column string `json:"column,omitempty" yaml:"column,omitempty"`

	// Window buckets the timestamp before comparing it. Zero compares exact
	// values, which is what groups the lines of one log flush together.
	Window types.Duration `json:"window,omitempty" yaml:"window,omitempty"`

	// Order declares how the rows are sorted on the way in: "asc" (default) or
	// "desc".
	Order string `json:"order,omitempty" yaml:"order,omitempty"`

	// Boundary is a CEL predicate over `row`, `prev` and `index` that returns
	// true when row starts a new batch. Setting it replaces the timestamp rule
	// entirely, for sources whose timestamps cannot group anything.
	Boundary string `json:"boundary,omitempty" yaml:"boundary,omitempty"`

	// Continuation is a CEL predicate over the same bindings that returns true
	// when row continues the batch above it. It vetoes the timestamp rule, so a
	// stack frame still folds in when the shipper stamped it a millisecond late.
	Continuation string `json:"continuation,omitempty" yaml:"continuation,omitempty"`

	// Max caps the rows in one batch (default 1000); the batch is closed at the
	// cap.
	Max int `json:"max,omitempty" yaml:"max,omitempty"`

	// When gates the transform: batches it rejects pass through untouched. Its
	// bindings are the batch scope below.
	When string `json:"when,omitempty" yaml:"when,omitempty"`

	// Keep chooses the row the merged row is built from: "first" (default) or
	// "last".
	Keep string `json:"keep,omitempty" yaml:"keep,omitempty"`

	// Set overrides columns on the merged row. Each value is a CEL expression
	// over the batch scope: `batch` (the rows, oldest first), `first`, `last`,
	// `count` and `row` (the kept row, unmodified while Set is evaluated, so the
	// result does not depend on map ordering).
	//
	// Iterating the batch needs `dyn(batch).map(...)` rather than
	// `batch.map(...)`: gomplate declares every expression variable as
	// cel.AnyType, and the CEL checker refuses a `google.protobuf.Any` as the
	// range of a comprehension. Indexing, `size()` and field selection need no
	// such conversion.
	Set map[string]string `json:"set,omitempty" yaml:"set,omitempty"`

	// Emit replaces Set for transforms that fan a batch out to several rows: a
	// CEL expression over the same scope returning a list of rows.
	Emit string `json:"emit,omitempty" yaml:"emit,omitempty"`
}

// Validate rejects a configuration that cannot mean one thing.
func (c BatchConfig) Validate() error {
	if c.Set != nil && c.Emit != "" {
		return fmt.Errorf("batch sets both set and emit; pick one")
	}
	if len(c.Set) == 0 && c.Emit == "" {
		return fmt.Errorf("batch requires either set or emit")
	}
	if c.Order != "" && c.Order != OrderAscending && c.Order != OrderDescending {
		return fmt.Errorf("batch order %q is not %q or %q", c.Order, OrderAscending, OrderDescending)
	}
	if c.Keep != "" && c.Keep != KeepFirst && c.Keep != KeepLast {
		return fmt.Errorf("batch keep %q is not %q or %q", c.Keep, KeepFirst, KeepLast)
	}
	if c.Max < 0 {
		return fmt.Errorf("batch max %d is negative", c.Max)
	}
	if c.Boundary != "" && c.Continuation != "" {
		return fmt.Errorf("batch sets both boundary and continuation; boundary already replaces the timestamp rule")
	}
	return nil
}

// batchLimit returns Max, defaulted.
func (c BatchConfig) batchLimit() int {
	if c.Max <= 0 {
		return DefaultBatchMax
	}
	return c.Max
}

// compiledBatch is a BatchConfig with its expressions compiled once and its
// timestamp column resolved against the actual rows.
type compiledBatch struct {
	*merger
	config       BatchConfig
	column       string
	boundary     *query.RowExpr
	continuation *query.RowExpr
}

func compileBatch(ctx context.Context, cfg BatchConfig, rows []query.Row) (*compiledBatch, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	merge, err := compileMerger(ctx, cfg.Keep, cfg.When, cfg.Emit, cfg.Set)
	if err != nil {
		return nil, err
	}
	compiled := &compiledBatch{merger: merge, config: cfg}

	for _, expression := range []struct {
		source string
		target **query.RowExpr
	}{
		{cfg.Boundary, &compiled.boundary},
		{cfg.Continuation, &compiled.continuation},
	} {
		if expression.source == "" {
			continue
		}
		expr, err := query.CompileRowExpr(ctx, expression.source)
		if err != nil {
			return nil, err
		}
		*expression.target = expr
	}

	if compiled.boundary == nil {
		column, err := resolveTimestampColumn(cfg.Column, rows)
		if err != nil {
			return nil, err
		}
		compiled.column = column
	}
	return compiled, nil
}

// resolveTimestampColumn picks the column the default grouping rule reads. An
// explicit column that no row carries is a configuration error, not a silent
// single-batch scan.
func resolveTimestampColumn(configured string, rows []query.Row) (string, error) {
	if len(rows) == 0 {
		return configured, nil
	}
	if configured != "" {
		for _, row := range rows {
			if _, ok := row[configured]; ok {
				return configured, nil
			}
		}
		return "", fmt.Errorf("timestamp column %q is on no row (available: %s)", configured, rowKeys(rows[0]))
	}
	for _, candidate := range timestampColumns {
		if _, ok := rows[0][candidate]; ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no timestamp column found (looked for %v, row has: %s); set column or boundary",
		timestampColumns, rowKeys(rows[0]))
}

func rowKeys(row query.Row) string {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}

// ApplyBatch groups rows into batches and collapses each one per cfg. Pure
// logic — no database, no provider.
func ApplyBatch(ctx context.Context, rows []query.Row, cfg BatchConfig) ([]query.Row, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	compiled, err := compileBatch(ctx, cfg, rows)
	if err != nil {
		return nil, err
	}

	ordered := rows
	if cfg.Order == OrderDescending {
		ordered = slices.Clone(rows)
		slices.Reverse(ordered)
	}

	batches, err := compiled.group(ordered)
	if err != nil {
		return nil, err
	}

	out := make([]query.Row, 0, len(batches))
	for index, batch := range batches {
		transformed, err := compiled.collapse(batch)
		if err != nil {
			return nil, fmt.Errorf("batch %d (%d rows): %w", index, len(batch), err)
		}
		out = append(out, transformed...)
	}

	if cfg.Order == OrderDescending {
		slices.Reverse(out)
	}
	return out, nil
}

// group walks the rows once, closing the current batch whenever the partition
// changes, the cap is reached, or the boundary rule fires.
func (c *compiledBatch) group(rows []query.Row) ([][]query.Row, error) {
	limit := c.config.batchLimit()
	var batches [][]query.Row
	var current []query.Row

	for index, row := range rows {
		if len(current) == 0 {
			current = []query.Row{row}
			continue
		}
		previous := current[len(current)-1]
		starts, err := c.startsBatch(row, previous, index, len(current) >= limit)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", index, err)
		}
		if starts {
			batches = append(batches, current)
			current = []query.Row{row}
			continue
		}
		current = append(current, row)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches, nil
}

func (c *compiledBatch) startsBatch(row, previous query.Row, index int, atLimit bool) (bool, error) {
	if partitionKey(row, c.config.Partition) != partitionKey(previous, c.config.Partition) {
		return true, nil
	}
	if atLimit {
		return true, nil
	}
	bindings := map[string]any{"row": map[string]any(row), "prev": map[string]any(previous), "index": index}
	if c.continuation != nil {
		continues, err := c.continuation.Bool(bindings)
		if err != nil {
			return false, err
		}
		if continues {
			return false, nil
		}
	}
	if c.boundary != nil {
		return c.boundary.Bool(bindings)
	}
	return c.timestampChanged(row, previous)
}

func (c *compiledBatch) timestampChanged(row, previous query.Row) (bool, error) {
	current, err := batchTimestamp(row, c.column, c.config.Window.Duration)
	if err != nil {
		return false, err
	}
	before, err := batchTimestamp(previous, c.column, c.config.Window.Duration)
	if err != nil {
		return false, err
	}
	return !current.Equal(before), nil
}

// --- Processor wrapper ------------------------------------------------------

func init() {
	query.RegisterProcessor(&batchProcessor{})
}

type batchProcessor struct{}

func (batchProcessor) Type() string { return "cel.batch" }

func (batchProcessor) Process(ctx context.Context, spec query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	cfg, err := query.DecodeOptions[BatchConfig](spec.Config)
	if err != nil {
		return nil, err
	}
	rows, err := ApplyBatch(ctx, in.Rows, cfg)
	if err != nil {
		return nil, err
	}
	return &query.Result{Profile: in.Profile, Rows: rows, Context: in.Context}, nil
}
