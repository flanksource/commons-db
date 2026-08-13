package query

import (
	"fmt"
	"iter"
	"slices"
	"sort"

	"github.com/flanksource/commons-db/context"
)

// ReconcileMode says how a run was joined, because the two ways differ in what
// they cost and in what they can promise.
type ReconcileMode string

const (
	// ReconcileMerged walked both sides in key order and joined them as they
	// arrived. Memory is one key's rows, not one dataset.
	ReconcileMerged ReconcileMode = "merged"

	// ReconcileBuffered read both sides in full and joined them in memory. It is
	// what a run falls back to when the key is not the order — a CEL key, or a
	// profile ordered by something else — because then no two rows can be known
	// to be adjacent without reading everything.
	ReconcileBuffered ReconcileMode = "buffered"
)

// Mergeable reports whether a run can be joined by walking both sides, and says
// why not when it cannot.
//
// The requirement is that the key is a prefix of both orders. Then rows sharing
// a key are adjacent on both sides, which is the whole basis of a merge join —
// a key can be finished and emitted as soon as a larger one appears, without
// holding anything else.
func (r ReconcileRun) Mergeable() (bool, string) {
	if len(r.Config.Key.Columns) == 0 {
		return false, "the key is a CEL expression, so no order can be known to group it"
	}
	for _, side := range []struct {
		name    string
		profile Profile
	}{{"source", r.Source}, {"dest", r.Dest}} {
		if err := side.profile.Order.Pageable(); err != nil {
			return false, fmt.Sprintf("%s profile %q: %s", side.name, side.profile.Name, err)
		}
		columns := side.profile.Order.Columns()
		if len(columns) < len(r.Config.Key.Columns) || !slices.Equal(columns[:len(r.Config.Key.Columns)], r.Config.Key.Columns) {
			return false, fmt.Sprintf(
				"%s profile %q is ordered by %v, which does not begin with the key %v, so rows sharing a key are not adjacent",
				side.name, side.profile.Name, columns, r.Config.Key.Columns)
		}
	}
	return true, ""
}

// mergeJoin walks both sides in key order and joins them as they arrive.
func mergeJoin(ctx context.Context, run ReconcileRun) (*ReconcileResult, error) {
	spec := run.Config.ReconcileSpec
	keyOf, err := spec.Key.Resolver(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	source, err := openReconcileSide(ctx, "source", run.Source, run.SourceFilters, keyOf, spec)
	if err != nil {
		return nil, err
	}
	defer source.close()
	dest, err := openReconcileSide(ctx, "dest", run.Dest, run.DestFilters, keyOf, spec)
	if err != nil {
		return nil, err
	}
	defer dest.close()

	result := &ReconcileResult{
		Config:        *run.Config.Clone(),
		SourceProfile: run.Source,
		DestProfile:   run.Dest,
		Source:        run.Source.Name,
		Dest:          run.Dest.Name,
		Mode:          ReconcileMerged,
	}

	keys := run.Config.Range
	for {
		left, err := source.seek(keys)
		if err != nil {
			return nil, err
		}
		right, err := dest.seek(keys)
		if err != nil {
			return nil, err
		}
		switch {
		case left == nil && right == nil:
			result.SourceTruncated = source.truncated
			result.DestTruncated = dest.truncated
			result.Provenance = &ReconcileProvenance{
				Mode:   ReconcileMerged,
				Source: source.recorder.finish(run.Source, source.rows, source.truncated),
				Dest:   dest.recorder.finish(run.Dest, dest.rows, dest.truncated),
				RanAt:  source.recorder.started,
			}
			return result, nil
		case right == nil || (left != nil && left.key < right.key):
			result.appendGroup(left.key, left.rows, nil)
			source.take()
		case left == nil || right.key < left.key:
			result.appendGroup(right.key, nil, right.rows)
			dest.take()
		default:
			result.appendGroup(left.key, left.rows, right.rows)
			source.take()
			dest.take()
		}
	}
}

// keyGroup is every consecutive row sharing one key on one side.
type keyGroup struct {
	key  string
	rows []keyedRow
}

// reconcileSide is one side of a merge join: a row sequence read in key order,
// with a one-group lookahead.
type reconcileSide struct {
	name       string
	profile    string
	next       func() (Row, error, bool)
	stop       func()
	keyOf      KeyFunc
	timeColumn string

	pending   *keyedRow
	pendKey   string
	group     *keyGroup
	lastKey   string
	hasLast   bool
	exhausted bool
	truncated bool

	recorder *reconcileSideRecorder
	// rows counts what the join actually pulled, which a key range can cut far
	// short of what the provider returned.
	rows int
}

func openReconcileSide(
	ctx context.Context,
	name string,
	profile Profile,
	params map[string]any,
	keyOf KeyFunc,
	spec ReconcileSpec,
) (*reconcileSide, error) {
	timeColumn, err := timeColumnOf(spec.TimeColumn, profile, nil)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %s: %w", name, err)
	}
	side := &reconcileSide{
		name:       name,
		profile:    profile.Name,
		keyOf:      keyOf,
		timeColumn: timeColumn,
		recorder:   newReconcileSideRecorder(name, profile, params),
	}
	// ExecutePages prefers the recorder on the request over the one on the
	// context, so a merged run needs no context sink at all and the two sides
	// cannot overwrite each other's record.
	request := walkRequest(profile, DefaultMaxPageSize)
	request.Diagnostics = side.recorder.sink
	side.recorder.start()
	pages := ExecutePages(ctx, profile, request, params)
	side.next, side.stop = iter.Pull2(rowsRecordingProgress(pages, side))
	return side, nil
}

// rowsRecordingProgress flattens pages while keeping the two facts that do not
// survive flattening: that a backend applied a cap of its own, and how many
// rows this side actually gave the join. A reconcile that lost the first would
// present a partial read as a complete diagnosis, which is the failure this
// whole path exists to prevent; the second is what makes a side's provenance
// comparable to the provider's own count.
func rowsRecordingProgress(pages iter.Seq2[Page, error], side *reconcileSide) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		for page, err := range pages {
			if err != nil {
				yield(nil, err)
				return
			}
			if page.Truncated {
				side.truncated = true
			}
			for _, row := range page.Rows {
				side.rows++
				if !yield(row, nil) {
					return
				}
			}
		}
	}
}

func (s *reconcileSide) close() { s.stop() }

// peek returns the next key's group without consuming it.
func (s *reconcileSide) peek() (*keyGroup, error) {
	if s.group != nil || s.exhausted {
		return s.group, nil
	}
	if err := s.fill(); err != nil {
		return nil, err
	}
	return s.group, nil
}

func (s *reconcileSide) take() { s.group = nil }

// seek advances past keys below the range and stops the side at its end, so a
// ranged run reads only the span it covers rather than filtering a full read.
func (s *reconcileSide) seek(keys *KeyRange) (*keyGroup, error) {
	for {
		group, err := s.peek()
		if err != nil || group == nil {
			return nil, err
		}
		if keys.After(group.key) {
			// The walk is ordered, so nothing further can be in range.
			s.exhausted = true
			s.group = nil
			return nil, nil
		}
		if !keys.Before(group.key) {
			return group, nil
		}
		s.take()
	}
}

// fill reads one whole key's worth of rows, stopping at the first row of the
// next key — which it keeps as the lookahead that starts the group after this
// one.
func (s *reconcileSide) fill() error {
	if s.pending == nil {
		loaded, err := s.read()
		if err != nil {
			return err
		}
		if loaded == nil {
			s.exhausted = true
			return nil
		}
	}
	group := &keyGroup{key: s.pendKey}
	for {
		group.rows = append(group.rows, *s.pending)
		s.pending = nil
		loaded, err := s.read()
		if err != nil {
			return err
		}
		if loaded == nil || s.pendKey != group.key {
			break
		}
	}

	// A merge join is only correct while both sides agree on what "next" means.
	// If a side hands back a key that sorts before one it already gave, the
	// backend's collation is not the one being compared here — and the join
	// would silently report matched keys as one-sided. It stops instead.
	if s.hasLast && group.key < s.lastKey {
		return fmt.Errorf(
			"reconcile: %s profile %q returned key %q after %q; its backend orders keys differently than this join compares them, so declare an order whose collation matches or the join would report matched keys as missing",
			s.name, s.profile, group.key, s.lastKey)
	}
	s.lastKey = group.key
	s.hasLast = true

	sortKeyedRowsByTime(group.rows)
	s.group = group
	return nil
}

// read pulls one row into pending, deriving its key and event time.
func (s *reconcileSide) read() (*keyedRow, error) {
	row, err, ok := s.next()
	if err != nil {
		return nil, fmt.Errorf("reconcile: %s profile %q: %w", s.name, s.profile, err)
	}
	if !ok {
		return nil, nil
	}
	key, err := s.keyOf(row)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %s profile %q: %w", s.name, s.profile, err)
	}
	at, err := rowTime(row, s.timeColumn)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %s profile %q: %w", s.name, s.profile, err)
	}
	s.pending = &keyedRow{row: row, at: at}
	s.pendKey = key
	return s.pending, nil
}

// sortKeyedRowsByTime orders a key's rows oldest-first, so the cartesian
// expansion pairs the first source attempt with the first destination arrival.
func sortKeyedRowsByTime(rows []keyedRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		switch {
		case rows[i].at == nil:
			return false
		case rows[j].at == nil:
			return true
		default:
			return rows[i].at.Before(*rows[j].at)
		}
	})
}
