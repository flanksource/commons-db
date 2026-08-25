package processor

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"

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
// It runs page by page too, but under a stated weaker reading: a group surfaces
// on the page it first appears on, carrying the count from that page alone, and
// the cursor remembers it so no later page repeats it. Revising an already-sent
// row is not something a walk can do — see ProcessPage.
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

// ProcessPage folds one page and suppresses every group an earlier page of the
// same walk already emitted.
//
// The whole-result fold this mirrors reads every row before any count is final.
// A walk cannot: a page is served before the next one is read, and a row already
// sent cannot be revised. So the streaming reading is a different — and stated —
// one. A group surfaces once, on the page where it first appears, and `count`
// is how many rows folded into it *on that page*. What the carried state buys is
// that the group does not appear again on page four having already appeared on
// page two, which is the thing that would make a paged log table unreadable.
func (dedupeProcessor) ProcessPage(
	ctx context.Context,
	spec query.ProcessorSpec,
	page query.Page,
	state []byte,
) (query.Page, []byte, error) {
	cfg, err := query.DecodeOptions[DedupeConfig](spec.Config)
	if err != nil {
		return query.Page{}, nil, err
	}
	rows, err := ApplyDedupe(ctx, page.Rows, cfg)
	if err != nil {
		return query.Page{}, nil, err
	}
	seen, err := decodeSeenKeys(state)
	if err != nil {
		return query.Page{}, nil, err
	}
	kept := make([]query.Row, 0, len(rows))
	for _, row := range rows {
		hash := hashPartitionKey(partitionKey(row, cfg.Partition))
		if _, already := seen[hash]; already {
			continue
		}
		// Added as it is kept rather than in a second pass, so a group the fold
		// left as several rows still surfaces all of them on this page.
		kept = append(kept, row)
	}
	for _, row := range rows {
		seen[hashPartitionKey(partitionKey(row, cfg.Partition))] = struct{}{}
	}
	page.Rows = kept
	return page, encodeSeenKeys(seen), nil
}

// Keys are carried as 64-bit hashes rather than as the keys themselves: a
// partition key is an arbitrary log message, and the cursor holding them has a
// size ceiling. A collision would suppress a group that deserved a row, which
// at 64 bits needs on the order of a billion groups to become likely — and
// MaxCursorBytes stops the walk long before that.
func hashPartitionKey(key string) uint64 {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(key))
	return digest.Sum64()
}

func decodeSeenKeys(state []byte) (map[uint64]struct{}, error) {
	if len(state)%8 != 0 {
		return nil, fmt.Errorf("carried dedupe state is %d bytes, which is not a whole number of keys", len(state))
	}
	seen := make(map[uint64]struct{}, len(state)/8)
	for offset := 0; offset < len(state); offset += 8 {
		seen[binary.BigEndian.Uint64(state[offset:offset+8])] = struct{}{}
	}
	return seen, nil
}

// encodeSeenKeys writes the set in sorted order so the same set always encodes
// to the same bytes — a cursor that changed while the walk did not would look
// like a different position every page.
func encodeSeenKeys(seen map[uint64]struct{}) []byte {
	if len(seen) == 0 {
		return nil
	}
	keys := make([]uint64, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	encoded := make([]byte, 0, len(keys)*8)
	for _, key := range keys {
		encoded = binary.BigEndian.AppendUint64(encoded, key)
	}
	return encoded
}
