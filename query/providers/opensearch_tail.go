package providers

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// openSearchTailBatch is how many hits one poll of a tail fetches. It is the
// backfill's page size rather than a cap on the tail: a poll that comes back
// full is answered by asking again immediately, so the batch only decides how
// much of the window one search carries.
const openSearchTailBatch = 1000

// openSearchDefaultTailPoll is how long a caught-up tail waits before asking
// again.
//
// It is two seconds because index.refresh_interval defaults to one: a document
// is not searchable at all until a refresh, so polling faster than that mostly
// buys empty searches. An index that refreshes slower should say so with
// options.tailPoll rather than have this guess at it.
const openSearchDefaultTailPoll = 2 * time.Second

// openSearchTailOrder is the order a tail reads in, which is not the order the
// profile is read in.
//
// A tail walks forward with search_after, and search_after only ever moves
// toward the end of the sort — so an index can be followed ascending and in no
// other direction. That is not a contradiction of NaturalOrder deriving
// `timeField desc`: a declared order says how to read a window of history, and
// newest-first is right for that, while a tail has no window and emits
// documents in the order they were written. Loki's Stream drops `direction` for
// the same reason.
//
// The tiebreaker is _id, exactly as NaturalOrder appends it and for the reasons
// given there — _shard_doc is numbered inside a point-in-time, and a tail must
// not open one.
func openSearchTailOrder(req query.ProviderRequest, opts opensearchOptions) (query.Order, error) {
	leading := ""
	if opts.Search != nil {
		leading = strings.TrimSpace(opts.Search.TimeField)
	}
	if leading == "" && len(req.Order) > 0 {
		// A raw-DSL profile gets no derived order, so whatever req.Order carries
		// is the author's own; its leading column is the only statement anything
		// here has about what this index is written in order of.
		leading = strings.TrimSpace(req.Order[0].Column)
	}
	if leading == "" || leading == openSearchTiebreaker {
		return nil, fmt.Errorf(
			"nothing here knows the order this index is written in, so a tail has no direction to follow; declare `provider.options.search.timeField` or an `order:` whose leading column is the document's timestamp")
	}
	return query.Order{
		{Column: leading},
		{Column: openSearchTiebreaker, Unique: true},
	}, nil
}

// openSearchTailSettings is what the options say about how to tail, already
// parsed.
type openSearchTailSettings struct {
	poll time.Duration
	lag  time.Duration
}

// openSearchTailOptions reads the tail controls off the profile.
//
// A malformed duration fails rather than falling back to the default: the
// difference between "poll every 30 seconds" and "poll every two" is the whole
// of what the author was configuring, and silently substituting one for the
// other is the bug they would never find.
func openSearchTailOptions(opts opensearchOptions) (openSearchTailSettings, error) {
	settings := openSearchTailSettings{poll: openSearchDefaultTailPoll}
	if raw := strings.TrimSpace(opts.TailPoll); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return settings, fmt.Errorf("tailPoll %q is not a duration: %w", opts.TailPoll, err)
		}
		if parsed <= 0 {
			return settings, fmt.Errorf("tailPoll %q must be positive; a tail that never waits is a busy loop against the index", opts.TailPoll)
		}
		settings.poll = parsed
	}
	if raw := strings.TrimSpace(opts.TailLag); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return settings, fmt.Errorf("tailLag %q is not a duration: %w", opts.TailLag, err)
		}
		if parsed < 0 {
			return settings, fmt.Errorf("tailLag %q must not be negative; a tail cannot read ahead of now", opts.TailLag)
		}
		settings.lag = parsed
	}
	return settings, nil
}

// openSearchTailBound renders the upper bound a lagging tail holds its cursor
// behind, or nil when options.tailLag is unset.
//
// The bound is what makes a tail robust against ingest that is not
// instantaneous. A document written now but indexed three seconds from now is
// invisible until it lands, and by then an unlagged cursor has already advanced
// past its timestamp — so it is never emitted. Holding the cursor at now-lag
// leaves it room to arrive first, at the cost of showing every line that much
// later.
//
// It needs the field's mapping to know whether the bound is spelled as a date
// or as an epoch integer, so a lag on a raw-DSL profile is refused rather than
// guessed at.
func openSearchTailBound(
	opts opensearchOptions,
	lag time.Duration,
	mapping *esdsl.TimeFieldMapping,
	now time.Time,
) (field string, value any, err error) {
	if lag == 0 {
		return "", nil, nil
	}
	if opts.Search == nil || strings.TrimSpace(opts.Search.TimeField) == "" {
		return "", nil, fmt.Errorf(
			"tailLag bounds the tail on a time field, so it requires `provider.options.search.timeField`; this profile declares none")
	}
	if mapping == nil {
		return "", nil, fmt.Errorf(
			"tailLag needs the mapping of timeField %q to spell its bound, and none was resolved; declare a time-from parameter so the field is inspected",
			opts.Search.TimeField)
	}
	value, err = esdsl.EncodeTimeBound(now.Add(-lag), *opts.Search, mapping)
	if err != nil {
		return "", nil, fmt.Errorf("tailLag bound for timeField %q: %w", opts.Search.TimeField, err)
	}
	return strings.TrimSpace(opts.Search.TimeField), value, nil
}

// applyOpenSearchTailBound ANDs the lag bound onto an already-built body.
//
// It is written as its own clause rather than folded in as a runtime filter
// because the filter compiler refuses two upper bounds on one field — which is
// the right answer for two selections that disagree, and the wrong one here,
// where a profile's declared trace window and the tail's lag are both meant to
// apply. Two range clauses inside one bool filter intersect, which is what
// "within the window, and at least lag old" means.
func applyOpenSearchTailBound(body map[string]any, field string, value any) {
	if field == "" {
		return
	}
	existing := body["query"]
	if existing == nil {
		existing = map[string]any{"match_all": map[string]any{}}
	}
	bound := map[string]any{"range": map[string]any{field: map[string]any{"lte": value}}}
	body["query"] = map[string]any{"bool": map[string]any{
		"filter": []any{existing, bound},
	}}
}

// tail follows the index by re-asking for whatever was written past the last
// document it emitted.
//
// It is byCursor's shape with the two things that make a walk a walk removed.
// There is no point-in-time, because a PIT freezes the view a walk depends on
// and a tail exists to see what lands after it started. And there is no end:
// a short page means caught up rather than finished, so it waits and asks
// again instead of returning.
//
// A poll that comes back full is answered immediately — that is the backfill
// draining, and sleeping through it would tail an hour-old window at one batch
// per interval forever.
func (w openSearchWalk) tail(
	ctx context.Context,
	req query.ProviderRequest,
	settings openSearchTailSettings,
	emit func(query.Row),
) error {
	// How many hits one poll asks for, learned once. The walk's own batch is not
	// that number — a profile's limit or search.size can narrow it — and
	// comparing a poll against the batch instead would read every poll of such a
	// profile as caught up and drain its backfill one interval at a time. It
	// does not depend on the position, so building once answers it for the whole
	// tail.
	probe, err := w.build(openSearchPage{size: openSearchTailBatch})
	if err != nil {
		return err
	}
	size := probe.limit

	after := req.Position.Keys
	for {
		raw, rows, _, err := w.search(ctx, openSearchPage{size: openSearchTailBatch, after: after})
		if err != nil {
			// Stopping a tail is how every tail ends, and the read failure that
			// cancellation raises is that stop rather than a fault. The error is
			// wrapped by the searcher and again by openSearchFailure, so the
			// context is asked directly rather than unwrapped for.
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, row := range rows {
			emit(row)
		}
		if len(rows) > 0 {
			if after, err = openSearchSortKeys(raw, req.Order); err != nil {
				return err
			}
		}
		// A debug run describes the search this tail opened with rather than
		// whichever poll happened to be last. Dropping the sink mutates only
		// this copy of the walk, which is the whole of the tail's lifetime.
		w.diagnostics = nil

		if size > 0 && len(rows) >= size {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(settings.poll):
		}
	}
}
