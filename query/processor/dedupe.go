package processor

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// DedupeConfig declares how rows are grouped by key and what each group
// collapses to. It is the configuration of the "cel.dedupe" processor.
//
// Grouping is a hash group-by over the whole result, which is the difference
// that matters against "cel.batch": batch groups runs of *adjacent* rows, so it
// folds a stack trace into the line that threw but never notices the same error
// recurring an hour later. Repeated log lines are scattered through a
// time-ordered result, so collapsing them needs every row in hand at once.
//
// That is also why this cannot be a PageProcessor — a row is not final until
// the last page has been read.
type DedupeConfig struct {
	// Partition is the dedup key: rows agreeing on every one of these columns
	// are one group. Required — an empty key would collapse the entire result
	// to a single row, which is never what an author means.
	Partition []string `json:"partition,omitempty" yaml:"partition,omitempty"`

	// Keep chooses which row of a group survives as the merged row: "first"
	// (default) or "last". Groups preserve arrival order, so with a newest-first
	// query "first" is the most recent occurrence.
	Keep string `json:"keep,omitempty" yaml:"keep,omitempty"`

	// When is a CEL predicate over `batch`, `first`, `last`, `count` and `row`
	// that gates the merge. A group it rejects passes through as separate rows,
	// so `count > 1` leaves non-duplicates exactly as they arrived.
	When string `json:"when,omitempty" yaml:"when,omitempty"`

	// Set assigns columns on the merged row from CEL over the same bindings —
	// `count` for how many rows collapsed, `dyn(batch)…` to reach across them.
	// Leaving it empty keeps the surviving row unchanged, which is the plain
	// "drop duplicates" behaviour.
	Set map[string]string `json:"set,omitempty" yaml:"set,omitempty"`

	// Emit replaces Set, returning the merged rows from one CEL expression.
	Emit string `json:"emit,omitempty" yaml:"emit,omitempty"`

	// Max caps how many rows one group may absorb. Rows past the cap start a
	// new group rather than being dropped.
	Max int `json:"max,omitempty" yaml:"max,omitempty"`
}

// Validate rejects a configuration that cannot mean one thing.
func (c DedupeConfig) Validate() error {
	if len(c.Partition) == 0 {
		return fmt.Errorf("dedupe requires partition; without a key every row collapses into one")
	}
	if c.Set != nil && c.Emit != "" {
		return fmt.Errorf("dedupe sets both set and emit; pick one")
	}
	if c.Keep != "" && c.Keep != KeepFirst && c.Keep != KeepLast {
		return fmt.Errorf("dedupe keep %q is not %q or %q", c.Keep, KeepFirst, KeepLast)
	}
	if c.Max < 0 {
		return fmt.Errorf("dedupe max %d is negative", c.Max)
	}
	return nil
}

// groupLimit returns Max, defaulted.
func (c DedupeConfig) groupLimit() int {
	if c.Max <= 0 {
		return DefaultBatchMax
	}
	return c.Max
}

// ApplyDedupe groups rows by their partition key and collapses each group per
// cfg. Pure logic — no database, no provider.
func ApplyDedupe(ctx context.Context, rows []query.Row, cfg DedupeConfig) ([]query.Row, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return rows, nil
	}

	merge, err := compileMerger(ctx, cfg.Keep, cfg.When, cfg.Emit, cfg.Set)
	if err != nil {
		return nil, err
	}

	out := make([]query.Row, 0, len(rows))
	for _, group := range groupByKey(rows, cfg.Partition, cfg.groupLimit()) {
		collapsed, err := merge.collapse(group)
		if err != nil {
			return nil, fmt.Errorf("group %q (%d rows): %w",
				partitionKey(group[0], cfg.Partition), len(group), err)
		}
		out = append(out, collapsed...)
	}
	return out, nil
}

// groupByKey buckets rows by partition key, keeping groups in first-seen order.
// Order is kept deliberately: a deduped log table should stay in the order the
// query asked for, with each line sitting where it first appeared.
func groupByKey(rows []query.Row, partition []string, limit int) [][]query.Row {
	var groups [][]query.Row
	open := map[string]int{}

	for _, row := range rows {
		key := partitionKey(row, partition)
		index, ok := open[key]
		if !ok {
			groups = append(groups, []query.Row{row})
			open[key] = len(groups) - 1
			continue
		}

		groups[index] = append(groups[index], row)

		// Past the cap the group is closed and the next row under this key
		// opens a fresh one, so a runaway key folds a page of rows rather than
		// the whole result.
		if len(groups[index]) >= limit {
			delete(open, key)
		}
	}
	return groups
}

// --- Processor wrapper ------------------------------------------------------

func init() {
	query.RegisterProcessor(&dedupeProcessor{})
}

type dedupeProcessor struct{}

func (dedupeProcessor) Type() string { return "cel.dedupe" }

func (dedupeProcessor) Process(ctx context.Context, spec query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	cfg, err := query.DecodeOptions[DedupeConfig](spec.Config)
	if err != nil {
		return nil, err
	}
	rows, err := ApplyDedupe(ctx, in.Rows, cfg)
	if err != nil {
		return nil, err
	}
	return &query.Result{Profile: in.Profile, Rows: rows, Context: in.Context}, nil
}
