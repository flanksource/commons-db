// Compaction in a sqlite file: dropping the rows a kind's rules select from
// the middle of its streams, mirroring that into an index, and merging
// streams into a new one.
package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

const (
	// compactBudget bounds how many rows one Compact evaluates, so a sweep
	// over a large file holds the writer in slices rather than at once.
	compactBudget = 10_000

	// deleteBatch is how many seqs one DELETE names.
	deleteBatch = 500
)

// compiledRule is a compact rule ready to evaluate: its CEL, and the instant
// before which a row is old enough, or nil for a rule with no age.
type compiledRule struct {
	where  *query.RowExpr
	before *time.Time
}

// Compact drops, from every stream of every kind with compact rules, the rows
// a rule selects, evaluating at most compactBudget rows, and reports how many
// it dropped. A stream's last row always stays. A derived index and a
// read-only backend compact nothing: the one mirrors its source, the other
// leaves the file to its writer.
func (b *Backend) Compact(ctx context.Context) (int, error) {
	if b.derived || b.readOnly.Load() {
		return 0, nil
	}
	kinds, err := b.compactedKinds(ctx)
	if err != nil {
		return 0, err
	}
	budget, removed := compactBudget, 0
	for _, kind := range kinds {
		table, err := b.kindTable(kind)
		if err != nil {
			return removed, err
		}
		rules, err := compileRules(ctx, table.schema, b.now())
		if err != nil {
			return removed, err
		}
		streams, err := b.kindStreams(ctx, kind)
		if err != nil {
			return removed, err
		}
		for _, stream := range streams {
			if budget <= 0 {
				return removed, nil
			}
			dropped, evaluated, err := b.compactStream(ctx, table, rules, stream, budget)
			removed, budget = removed+dropped, budget-evaluated
			if err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

// compactedKinds are the kinds the file holds whose schema has compact rules;
// a kind this build does not declare is another build's to compact.
func (b *Backend) compactedKinds(ctx context.Context) ([]string, error) {
	rows, err := b.database.Reader().QueryContext(ctx, `SELECT kind FROM record_kinds ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("sqlite record store %s: list kinds to compact: %w", b.Path(), err)
	}
	defer func() { _ = rows.Close() }()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		if schema, err := recordstore.ResolveKind(b.schema, kind); err == nil && len(schema.Options.Compact) > 0 {
			kinds = append(kinds, kind)
		}
	}
	return kinds, rows.Err()
}

func (b *Backend) kindStreams(ctx context.Context, kind string) ([]string, error) {
	rows, err := b.database.Reader().QueryContext(ctx, `SELECT stream_id FROM record_streams WHERE kind = ? ORDER BY stream_id`, kind)
	if err != nil {
		return nil, fmt.Errorf("kind %q: list streams: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	var streams []string
	for rows.Next() {
		var stream string
		if err := rows.Scan(&stream); err != nil {
			return nil, err
		}
		streams = append(streams, stream)
	}
	return streams, rows.Err()
}

func compileRules(ctx context.Context, schema recordstore.KindSchema, now time.Time) ([]compiledRule, error) {
	rules := make([]compiledRule, len(schema.Options.Compact))
	for index, rule := range schema.Options.Compact {
		if rule.Where != "" {
			where, err := query.CompileRowExpr(dbcontext.NewContext(ctx), rule.Where)
			if err != nil {
				return nil, fmt.Errorf("kind %q compact rule %d: %w", schema.Kind, index, err)
			}
			rules[index].where = where
		}
		if rule.OlderThan > 0 {
			before := now.Add(-rule.OlderThan)
			rules[index].before = &before
		}
	}
	return rules, nil
}

// compactStream drops the rows of stream below its high seq that a rule
// selects, reading at most budget of them, and reports how many it dropped and
// how many it read.
func (b *Backend) compactStream(ctx context.Context, table kindTable, rules []compiledRule, stream string, budget int) (int, int, error) {
	meta, err := b.Meta(ctx, stream)
	if errors.Is(err, recordstore.ErrNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	candidates, err := b.readRows(ctx, table, stream, 0, meta.HighSeq, budget)
	if err != nil {
		return 0, 0, err
	}
	var dropped []int64
	for _, row := range candidates {
		selected, err := selectedByRule(table, rules, row)
		if err != nil {
			return 0, len(candidates), fmt.Errorf("stream %q seq %v: %w", stream, row[seqColumn], err)
		}
		if selected {
			dropped = append(dropped, row[seqColumn].(int64))
		}
	}
	if len(dropped) == 0 {
		return 0, len(candidates), nil
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	var removed int64
	err = b.database.Write(func(writer *sql.DB) error {
		return inTx(ctx, writer, fmt.Sprintf("stream %q: compact", stream), func(tx *sql.Tx) error {
			current, err := loadMeta(ctx, tx, stream)
			if err != nil {
				return err
			}
			if current.Generation != meta.Generation {
				return nil
			}
			var compacted recordstore.Meta
			compacted, removed, err = deleteSeqsTx(ctx, tx, table.Table, current, dropped)
			if err != nil || removed == 0 {
				return err
			}
			compacted.Compactions++
			return upsertMeta(ctx, tx, compacted)
		})
	})
	if err == nil && removed > 0 {
		logger.V(1).Infof("sqlite record store %s: compacted %d rows of stream %q", b.Path(), removed, stream)
	}
	return int(removed), len(candidates), err
}

// selectedByRule reports whether any rule selects row: its age, if the rule
// has one, and its CEL, if it has one.
func selectedByRule(table kindTable, rules []compiledRule, row query.Row) (bool, error) {
	for _, rule := range rules {
		if rule.before != nil {
			stamped, ok := row[table.schema.Options.TimeColumn].(string)
			if !ok || stamped >= sqlitetable.FormatTime(*rule.before) {
				continue
			}
		}
		if rule.where != nil {
			bound := make(query.Row, len(row))
			for key, value := range row {
				if key != streamColumn && key != seqColumn {
					bound[key] = value
				}
			}
			selected, err := rule.where.Bool(map[string]any{"row": bound})
			if err != nil || !selected {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

// readRows reads up to limit rows of stream with afterSeq < seq < beforeSeq
// in seq order, structured columns decoded; zero beforeSeq bounds nothing.
func (b *Backend) readRows(ctx context.Context, table kindTable, stream string, afterSeq, beforeSeq int64, limit int) ([]query.Row, error) {
	streamID, err := table.Physical(streamColumn)
	if err != nil {
		return nil, err
	}
	seq, err := table.Physical(seqColumn)
	if err != nil {
		return nil, err
	}
	statement := table.Select() + fmt.Sprintf(` WHERE %s = ? AND %s > ?`, streamID, seq)
	args := []any{stream, afterSeq}
	if beforeSeq > 0 {
		statement += fmt.Sprintf(` AND %s < ?`, seq)
		args = append(args, beforeSeq)
	}
	statement += fmt.Sprintf(` ORDER BY %s LIMIT ?`, seq)
	args = append(args, limit)
	rows, err := b.database.Reader().QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("stream %q: read rows: %w", stream, err)
	}
	defer func() { _ = rows.Close() }()
	page, err := db.ScanRows[query.Row](rows)
	if err != nil {
		return nil, fmt.Errorf("stream %q: read rows: %w", stream, err)
	}
	for _, row := range page {
		if err := sqlitetable.DecodeStructured(table.Columns, row); err != nil {
			return nil, fmt.Errorf("stream %q: %w", stream, err)
		}
	}
	return page, nil
}

// deleteSeqsTx drops meta's rows at seqs in tx, returning its metadata after:
// its total less the rows it dropped, and its low seq at the first row left.
func deleteSeqsTx(ctx context.Context, tx *sql.Tx, table sqlitetable.Table, meta recordstore.Meta, seqs []int64) (recordstore.Meta, int64, error) {
	streamID, err := table.Physical(streamColumn)
	if err != nil {
		return recordstore.Meta{}, 0, err
	}
	seq, err := table.Physical(seqColumn)
	if err != nil {
		return recordstore.Meta{}, 0, err
	}
	var removed int64
	for batch := range slices.Chunk(seqs, deleteBatch) {
		markers := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := []any{meta.Stream}
		for _, value := range batch {
			args = append(args, value)
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ? AND %s IN (%s)`,
			sqlitetable.QuoteIdentifier(table.Name), streamID, seq, markers), args...)
		if err != nil {
			return recordstore.Meta{}, 0, fmt.Errorf("stream %q: drop rows: %w", meta.Stream, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return recordstore.Meta{}, 0, err
		}
		removed += affected
	}
	meta.Total -= removed
	var low sql.NullInt64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT MIN(%s) FROM %s WHERE %s = ?`, seq, sqlitetable.QuoteIdentifier(table.Name), streamID),
		meta.Stream).Scan(&low); err != nil {
		return recordstore.Meta{}, 0, fmt.Errorf("stream %q: find its first row: %w", meta.Stream, err)
	}
	if low.Valid {
		meta.LowSeq = max(meta.LowSeq, low.Int64)
	} else {
		meta.LowSeq = meta.HighSeq + 1
	}
	return meta, removed, nil
}

// DeleteSeqs mirrors a source compaction into the index: it drops the indexed
// rows at seqs and records the source's compaction count.
func (b *Backend) DeleteSeqs(ctx context.Context, stream string, seqs []int64, compactions int64) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	var meta recordstore.Meta
	err := b.database.Write(func(writer *sql.DB) error {
		return inTx(ctx, writer, fmt.Sprintf("stream %q: drop compacted rows", stream), func(tx *sql.Tx) error {
			current, err := b.openStoredStream(ctx, tx, stream, b.now())
			if err != nil {
				return err
			}
			table, err := storeTable(ctx, tx, current.Kind)
			if err != nil {
				return fmt.Errorf("stream %q: %w", stream, err)
			}
			if meta, _, err = deleteSeqsTx(ctx, tx, table, current, seqs); err != nil {
				return err
			}
			meta.Compactions = compactions
			return upsertMeta(ctx, tx, meta)
		})
	})
	return meta, err
}

// Merge creates target from the rows of sources; see recordstore.Merger.
func (b *Backend) Merge(ctx context.Context, target string, sources []string, options recordstore.MergeOptions) (recordstore.AppendResult, error) {
	if err := recordstore.ValidateStream(target); err != nil {
		return recordstore.AppendResult{}, err
	}
	if len(sources) == 0 {
		return recordstore.AppendResult{}, fmt.Errorf("merge into %q names no sources", target)
	}
	if _, err := b.Meta(ctx, target); err == nil {
		return recordstore.AppendResult{}, fmt.Errorf("merge target %q already exists", target)
	} else if !errors.Is(err, recordstore.ErrNotFound) {
		return recordstore.AppendResult{}, err
	}
	kind, err := b.sourcesKind(ctx, sources)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	table, err := b.kindTable(kind)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	rows, err := b.mergedRows(ctx, table, sources, options.Where)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	table, write, err := b.planAppend(ctx, table, target, rows)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	defer b.lockStreams(streamEntries(append([]string{target}, sources...)))()
	var result recordstore.AppendResult
	err = b.database.Write(func(writer *sql.DB) error {
		return inTx(ctx, writer, fmt.Sprintf("merge into %q", target), func(tx *sql.Tx) error {
			var err error
			if result, err = b.appendTx(ctx, tx, table, target, write, b.now()); err != nil || !options.DeleteSources {
				return err
			}
			for _, source := range sources {
				if err := removeStreamTx(ctx, tx, source, kind); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return result, err
}

// sourcesKind is the one kind every source holds.
func (b *Backend) sourcesKind(ctx context.Context, sources []string) (string, error) {
	var kind string
	for _, source := range sources {
		meta, err := b.Meta(ctx, source)
		if err != nil {
			return "", fmt.Errorf("merge source: %w", err)
		}
		if kind != "" && meta.Kind != kind {
			return "", fmt.Errorf("merge sources hold kind %q and kind %q; a stream holds one kind", kind, meta.Kind)
		}
		kind = meta.Kind
	}
	return kind, nil
}

// mergedRow is a source row with where it came from, for ordering.
type mergedRow struct {
	row  recordstore.Row
	rank int
	seq  int64
}

// mergedRows are the rows of sources where selects, ordered by the kind's
// time column, then source, then seq, with a key several hold resolved by the
// kind's conflict policy.
func (b *Backend) mergedRows(ctx context.Context, table kindTable, sources []string, where string) ([]recordstore.Row, error) {
	var filter *query.RowExpr
	if where != "" {
		var err error
		if filter, err = query.CompileRowExpr(dbcontext.NewContext(ctx), where); err != nil {
			return nil, err
		}
	}
	var merged []mergedRow
	for rank, source := range sources {
		err := b.Scan(ctx, source, 0, func(seq int64, row recordstore.Row) error {
			if filter != nil {
				selected, err := filter.Bool(map[string]any{"row": row})
				if err != nil || !selected {
					return err
				}
			}
			merged = append(merged, mergedRow{row: row, rank: rank, seq: seq})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("merge source %q: %w", source, err)
		}
	}
	timeColumn := table.schema.Options.TimeColumn
	slices.SortStableFunc(merged, func(a, b mergedRow) int {
		if timeColumn != "" {
			if order := cmp.Compare(fmt.Sprint(a.row[timeColumn]), fmt.Sprint(b.row[timeColumn])); order != 0 {
				return order
			}
		}
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.seq, b.seq))
	})
	if key := table.schema.Options.Key; key != "" {
		merged = oncePerKey(merged, key, table.schema.Options.OnConflict == recordstore.OnConflictReplace)
	}
	rows := make([]recordstore.Row, len(merged))
	for index, entry := range merged {
		rows[index] = entry.row
	}
	return rows, nil
}

// oncePerKey keeps one row per key: the first, or with last the last.
func oncePerKey(rows []mergedRow, key string, last bool) []mergedRow {
	keep := map[any]int{}
	for index, entry := range rows {
		if _, seen := keep[entry.row[key]]; !seen || last {
			keep[entry.row[key]] = index
		}
	}
	kept := make([]mergedRow, 0, len(keep))
	for index, entry := range rows {
		if keep[entry.row[key]] == index {
			kept = append(kept, entry)
		}
	}
	return kept
}

// streamEntries are batch entries naming streams, for lockStreams.
func streamEntries(streams []string) []recordstore.BatchEntry {
	entries := make([]recordstore.BatchEntry, len(streams))
	for index, stream := range streams {
		entries[index] = recordstore.BatchEntry{Stream: stream}
	}
	return entries
}
