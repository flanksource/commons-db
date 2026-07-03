package processor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// Reconciliation status values written to the ReconStatusColumn.
const (
	ReconAdded     = "added"
	ReconRemoved   = "removed"
	ReconChanged   = "changed"
	ReconUnchanged = "unchanged"

	// ReconStatusColumn holds the per-row reconciliation status.
	ReconStatusColumn = "_recon_status"

	// ReconChangesColumn holds a map of changed columns -> {from,to} for
	// rows with status "changed".
	ReconChangesColumn = "_recon_changes"
)

// ReconOptions configures a reconciliation.
type ReconOptions struct {
	// Key is the set of columns that uniquely identify a row across both sets.
	Key []string

	// Compare restricts which columns are compared for the "changed" status.
	// When empty, all non-key columns present in either row are compared.
	Compare []string
}

// Recon reconciles a target result set against a baseline, keyed by opts.Key.
// Each returned row is the target row (or the baseline row, for removals) plus a
// ReconStatusColumn of added/removed/changed/unchanged. Changed rows also carry
// a ReconChangesColumn mapping each differing column to {from, to}.
//
// Pure logic — no database required.
func Recon(baseline, target []query.Row, opts ReconOptions) ([]query.Row, error) {
	if len(opts.Key) == 0 {
		return nil, fmt.Errorf("recon requires at least one key column")
	}

	baseIndex := indexByKey(baseline, opts.Key)
	targetIndex := indexByKey(target, opts.Key)

	out := make([]query.Row, 0, len(target))

	// Walk target rows in order: added, changed, or unchanged.
	for _, trow := range target {
		key := keyOf(trow, opts.Key)
		brow, existed := baseIndex[key]
		if !existed {
			out = append(out, withStatus(trow, ReconAdded))
			continue
		}

		changes := diff(brow, trow, opts)
		if len(changes) == 0 {
			out = append(out, withStatus(trow, ReconUnchanged))
			continue
		}

		row := withStatus(trow, ReconChanged)
		row[ReconChangesColumn] = changes
		out = append(out, row)
	}

	// Walk baseline rows in order: anything not in target is removed.
	for _, brow := range baseline {
		if _, stillPresent := targetIndex[keyOf(brow, opts.Key)]; !stillPresent {
			out = append(out, withStatus(brow, ReconRemoved))
		}
	}

	return out, nil
}

// compareColumns returns the columns to compare: the explicit Compare list, or
// the union of non-key columns across both rows.
func compareColumns(base, target query.Row, opts ReconOptions) []string {
	if len(opts.Compare) > 0 {
		return opts.Compare
	}

	keySet := map[string]bool{}
	for _, k := range opts.Key {
		keySet[k] = true
	}

	colSet := map[string]bool{}
	for k := range base {
		colSet[k] = true
	}
	for k := range target {
		colSet[k] = true
	}

	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		if !keySet[k] {
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	return cols
}

func diff(base, target query.Row, opts ReconOptions) map[string]any {
	changes := map[string]any{}
	for _, col := range compareColumns(base, target, opts) {
		if !equalValues(base[col], target[col]) {
			changes[col] = map[string]any{"from": base[col], "to": target[col]}
		}
	}
	return changes
}

func equalValues(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func indexByKey(rows []query.Row, key []string) map[string]query.Row {
	idx := make(map[string]query.Row, len(rows))
	for _, row := range rows {
		idx[keyOf(row, key)] = row
	}
	return idx
}

func keyOf(row query.Row, key []string) string {
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = fmt.Sprintf("%v", row[k])
	}
	return strings.Join(parts, "\x00")
}

// withStatus returns a shallow copy of row with the reconciliation status set.
func withStatus(row query.Row, status string) query.Row {
	out := make(query.Row, len(row))
	for k, v := range row {
		out[k] = v
	}
	out[ReconStatusColumn] = status
	return out
}

// --- Processor wrappers -----------------------------------------------------

func init() {
	query.RegisterProcessor(&mergeProcessor{})
	query.RegisterProcessor(&reconProcessor{})
}

type mergeProcessor struct{}

func (mergeProcessor) Type() string { return "sqlite.merge" }

type mergeConfig struct {
	// SQL is the join/aggregation query run across the loaded tables.
	SQL string `json:"sql"`

	// As names the table the input Result is loaded as (default "main").
	As string `json:"as"`

	// With supplies additional named tables (inline rows) to join against.
	With map[string][]query.Row `json:"with"`
}

func (mergeProcessor) Process(ctx context.Context, spec query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	cfg, err := query.DecodeOptions[mergeConfig](spec.Config)
	if err != nil {
		return nil, err
	}
	if cfg.As == "" {
		cfg.As = "main"
	}

	sets := []ResultSet{{Name: cfg.As, Rows: in.Rows}}
	for name, rows := range cfg.With {
		sets = append(sets, ResultSet{Name: name, Rows: rows})
	}

	merged, err := Merge(ctx, cfg.SQL, sets...)
	if err != nil {
		return nil, err
	}

	return &query.Result{Profile: in.Profile, Rows: merged, Context: in.Context}, nil
}

type reconProcessor struct{}

func (reconProcessor) Type() string { return "sqlite.recon" }

type reconConfig struct {
	Key      []string    `json:"key"`
	Compare  []string    `json:"compare"`
	Baseline []query.Row `json:"baseline"`
}

func (reconProcessor) Process(_ context.Context, spec query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	cfg, err := query.DecodeOptions[reconConfig](spec.Config)
	if err != nil {
		return nil, err
	}

	rows, err := Recon(cfg.Baseline, in.Rows, ReconOptions{Key: cfg.Key, Compare: cfg.Compare})
	if err != nil {
		return nil, err
	}

	return &query.Result{Profile: in.Profile, Rows: rows, Context: in.Context}, nil
}
