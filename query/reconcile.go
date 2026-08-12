package query

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/context"
)

// ReconcileStatus is the outcome of joining one key across two Results.
type ReconcileStatus string

const (
	// ReconcileMatched means the key appears on both sides.
	ReconcileMatched ReconcileStatus = "matched"

	// ReconcileOnlySource means the key appears in the source only — it left A
	// and never arrived at B.
	ReconcileOnlySource ReconcileStatus = "only_source"

	// ReconcileOnlyDest means the key appears in the destination only — it
	// arrived at B without a matching record in A.
	ReconcileOnlyDest ReconcileStatus = "only_dest"
)

// ReconcileSpec configures a join between two Results.
//
// Reconcile answers a different question from the sqlite.recon processor: recon
// diffs two snapshots of the *same* schema cell by cell, whereas Reconcile joins
// two *different* profiles on a shared identity and reports presence and
// latency. Their statuses are deliberately not interchangeable.
type ReconcileSpec struct {
	// Range narrows the join to a span of keys. An empty range covers all of
	// them.
	//
	// It replaced a per-side row cap, which could not be made correct: two sides
	// cut at N rows each are two different sets of keys unless they happen to be
	// ordered identically, so the bound itself produced the one-sided keys the
	// run then reported as findings. A range cuts both sides at the same keys by
	// construction, so a key missing from one side inside it is missing.
	Range *KeyRange `json:"range,omitempty" yaml:"range,omitempty"`

	// Key derives the join identity from a row on either side. Cross-profile
	// joins normally need KeySpec.CEL, since the two sides rarely name the same
	// field the same way.
	Key KeySpec `json:"key" yaml:"key"`

	// TimeColumn names the row key holding each side's event time, used for the
	// source/dest timestamps and their difference. When empty it is discovered
	// from the profile column declared with Kind: timestamp; when no such column
	// exists, the time fields are simply omitted.
	TimeColumn string `json:"timeColumn,omitempty" yaml:"timeColumn,omitempty"`
}

// ReconcileConfig is a whole reconcile: which profile to join against, how to
// filter each side, and how to derive the identity. It is what a Profile stores
// under `reconcile:` and what the action's flags override, whereas ReconcileSpec
// is only what the join itself needs.
type ReconcileConfig struct {
	// Dest names the profile the source is reconciled against.
	Dest string `json:"dest" yaml:"dest"`

	// SourceFilters and DestFilters are independent because both profiles can
	// expose the same filter name with different values, or entirely different
	// filter surfaces.
	SourceFilters map[string]string `json:"sourceFilters,omitempty" yaml:"sourceFilters,omitempty"`
	DestFilters   map[string]string `json:"destFilters,omitempty" yaml:"destFilters,omitempty"`

	// Key and TimeColumn are promoted, so a stored reconcile reads as one flat
	// block rather than nesting the join spec inside itself.
	ReconcileSpec `yaml:",inline"`
}

// Clone returns a deep copy, so merging two profiles never aliases a stored
// config's filters or key columns.
func (c *ReconcileConfig) Clone() *ReconcileConfig {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.Key.Columns = append([]string(nil), c.Key.Columns...)
	cloned.SourceFilters = cloneReconcileFilters(c.SourceFilters)
	cloned.DestFilters = cloneReconcileFilters(c.DestFilters)
	return &cloned
}

func cloneReconcileFilters(filters map[string]string) map[string]string {
	if filters == nil {
		return nil
	}
	cloned := make(map[string]string, len(filters))
	for key, value := range filters {
		cloned[key] = value
	}
	return cloned
}

// MergeReconcileConfig layers an imported profile's reconcile with the
// importing profile's, field by field — the same way a replay block merges, so
// a profile can inherit a join and change only its bound or one filter.
func MergeReconcileConfig(base, override *ReconcileConfig) *ReconcileConfig {
	if override == nil {
		return base.Clone()
	}
	if base == nil {
		return override.Clone()
	}
	merged := base.Clone()
	if override.Dest != "" {
		merged.Dest = override.Dest
	}
	if override.Range != nil {
		merged.Range = override.Range.Clone()
	}
	if override.TimeColumn != "" {
		merged.TimeColumn = override.TimeColumn
	}
	// The key is one choice of identity, not a set of fields to blend: a
	// columns key and a CEL key cannot be half-overridden without producing the
	// both-set state the engine rejects.
	if len(override.Key.Columns) > 0 || override.Key.CEL != "" {
		merged.Key = override.Key
		merged.Key.Columns = append([]string(nil), override.Key.Columns...)
	}
	mergeReconcileFilters(&merged.SourceFilters, override.SourceFilters)
	mergeReconcileFilters(&merged.DestFilters, override.DestFilters)
	return merged
}

func mergeReconcileFilters(target *map[string]string, override map[string]string) {
	for name, value := range override {
		if *target == nil {
			*target = map[string]string{}
		}
		(*target)[name] = value
	}
}

// ReconcileRun is one execution of a ReconcileConfig: the two resolved profiles
// and each side's independently validated filter values.
type ReconcileRun struct {
	Source        Profile
	Dest          Profile
	Config        ReconcileConfig
	SourceFilters map[string]any
	DestFilters   map[string]any
}

// ReconcileProfiles runs both sides and joins them.
//
// When the key is a prefix of both orders the join is a merge: both sides are
// walked in key order and each key is emitted once it can no longer grow, so a
// run holds one key's rows rather than two datasets. Otherwise there is no way
// to know a key is finished without having read everything, and the run says
// which it did.
func ReconcileProfiles(ctx context.Context, run ReconcileRun) (*ReconcileResult, error) {
	if mergeable, _ := run.Mergeable(); mergeable {
		return mergeJoin(ctx, run)
	}

	source, err := Execute(ctx, run.Source, run.SourceFilters)
	if err != nil {
		return nil, fmt.Errorf("source profile %q: %w", run.Source.Name, err)
	}
	dest, err := Execute(ctx, run.Dest, run.DestFilters)
	if err != nil {
		return nil, fmt.Errorf("dest profile %q: %w", run.Dest.Name, err)
	}
	result, err := Reconcile(ctx, source, dest, run.Source, run.Dest, run.Config.ReconcileSpec)
	if err != nil {
		return nil, err
	}
	result.Mode = ReconcileBuffered
	result.Range = run.Config.Range
	_, result.BufferedReason = run.Mergeable()
	// A backend that capped either read leaves keys unaccounted for, and a
	// one-sided key inside an incomplete read is not a finding.
	result.SourceTruncated = source.Truncated
	result.DestTruncated = dest.Truncated
	return result, nil
}

// ReconcileStats summarises a reconcile run. Counts are per key, not per emitted
// row: a key with 2 source and 3 destination rows counts once as matched while
// emitting 6 rows.
type ReconcileStats struct {
	Matched    int `json:"matched"`
	OnlySource int `json:"only_source"`
	OnlyDest   int `json:"only_dest"`
	DupKeys    int `json:"dup_keys"`
}

// ReconcileRow is one joined pair. Source and Dest are nil on the side the key
// is missing from.
type ReconcileRow struct {
	Key    string          `json:"key"`
	Status ReconcileStatus `json:"status"`

	Source Row `json:"source,omitempty"`
	Dest   Row `json:"dest,omitempty"`

	SourceTime *time.Time     `json:"source_time,omitempty"`
	DestTime   *time.Time     `json:"dest_time,omitempty"`
	TimeDiff   *time.Duration `json:"time_diff,omitempty"`

	// Dup counters expose the cartesian expansion: index i of N rows sharing
	// this key on that side. Both are 1/1 when the key is unique.
	SourceDupIndex int `json:"source_dup_index,omitempty"`
	SourceDupCount int `json:"source_dup_count,omitempty"`
	DestDupIndex   int `json:"dest_dup_index,omitempty"`
	DestDupCount   int `json:"dest_dup_count,omitempty"`
}

// ReconcileResult is a completed join, carrying both profiles so the table
// renderer can label and order each side's columns.
type ReconcileResult struct {
	Spec          ReconcileSpec  `json:"spec"`
	SourceProfile Profile        `json:"-"`
	DestProfile   Profile        `json:"-"`
	Source        string         `json:"source"`
	Dest          string         `json:"dest"`
	Rows          []ReconcileRow `json:"rows"`
	Stats         ReconcileStats `json:"stats"`

	// Mode says how the two sides were joined, and BufferedReason says why a run
	// could not be merged — which is always something the author can change.
	Mode           ReconcileMode `json:"mode,omitempty"`
	BufferedReason string        `json:"buffered_reason,omitempty"`

	// Range is the span of keys this run covered; nil means all of them. Both
	// sides are cut at the same keys, so a one-sided key inside the range is a
	// finding rather than an artefact of where the read stopped.
	Range *KeyRange `json:"range,omitempty"`

	// The Truncated flags report a side whose backend cut the read short. A
	// one-sided key from an incomplete read is not a finding, which is why they
	// travel with the result.
	SourceTruncated bool `json:"source_truncated,omitempty"`
	DestTruncated   bool `json:"dest_truncated,omitempty"`
}

// Bounded reports whether either side was cut short by its backend, leaving
// keys unaccounted for.
func (r *ReconcileResult) Bounded() bool { return r.SourceTruncated || r.DestTruncated }

// Reconcile joins two Results on spec.Key.
//
// Every key present on either side produces at least one row. When a key repeats
// on a side, the pairs are expanded cartesian-style — 2 source rows against 3
// destination rows emit 6 rows — because with a duplicated identity there is no
// principled way to decide which source belongs to which destination, and
// silently picking one hides the ambiguity that is usually the actual bug.
func Reconcile(ctx context.Context, source, dest *Result, sourceProfile, destProfile Profile, spec ReconcileSpec) (*ReconcileResult, error) {
	if source == nil || dest == nil {
		return nil, fmt.Errorf("reconcile requires both a source and a destination result")
	}
	keyOf, err := spec.Key.Resolver(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	sourceTime, err := timeColumnOf(spec.TimeColumn, sourceProfile, source.Rows)
	if err != nil {
		return nil, fmt.Errorf("reconcile: source: %w", err)
	}
	destTime, err := timeColumnOf(spec.TimeColumn, destProfile, dest.Rows)
	if err != nil {
		return nil, fmt.Errorf("reconcile: dest: %w", err)
	}

	sourceGroups, err := groupRowsByKey(source.Rows, keyOf, sourceTime)
	if err != nil {
		return nil, fmt.Errorf("reconcile: source keys: %w", err)
	}
	destGroups, err := groupRowsByKey(dest.Rows, keyOf, destTime)
	if err != nil {
		return nil, fmt.Errorf("reconcile: dest keys: %w", err)
	}

	result := &ReconcileResult{
		Spec:          spec,
		SourceProfile: sourceProfile,
		DestProfile:   destProfile,
		Source:        sourceProfile.Name,
		Dest:          destProfile.Name,
	}

	for _, key := range unionKeys(sourceGroups, destGroups) {
		// The range applies here as well as on a merged run, so the two modes
		// answer the same question and only differ in what they had to read.
		if !spec.Range.Contains(key) {
			continue
		}
		result.appendGroup(key, sourceGroups[key], destGroups[key])
	}

	return result, nil
}

// appendGroup emits one key's rows. It is shared by both join modes so a merged
// run and a buffered one cannot disagree about what a key's result looks like.
func (r *ReconcileResult) appendGroup(key string, sourceRows, destRows []keyedRow) {
	if len(sourceRows) > 1 || len(destRows) > 1 {
		r.Stats.DupKeys++
	}

	switch {
	case len(sourceRows) > 0 && len(destRows) > 0:
		r.Stats.Matched++
		for i, s := range sourceRows {
			for j, d := range destRows {
				r.Rows = append(r.Rows, ReconcileRow{
					Key:            key,
					Status:         ReconcileMatched,
					Source:         s.row,
					Dest:           d.row,
					SourceTime:     s.at,
					DestTime:       d.at,
					TimeDiff:       timeDiff(s.at, d.at),
					SourceDupIndex: i + 1,
					SourceDupCount: len(sourceRows),
					DestDupIndex:   j + 1,
					DestDupCount:   len(destRows),
				})
			}
		}
	case len(sourceRows) > 0:
		r.Stats.OnlySource++
		for i, s := range sourceRows {
			r.Rows = append(r.Rows, ReconcileRow{
				Key:            key,
				Status:         ReconcileOnlySource,
				Source:         s.row,
				SourceTime:     s.at,
				SourceDupIndex: i + 1,
				SourceDupCount: len(sourceRows),
			})
		}
	default:
		r.Stats.OnlyDest++
		for j, d := range destRows {
			r.Rows = append(r.Rows, ReconcileRow{
				Key:          key,
				Status:       ReconcileOnlyDest,
				Dest:         d.row,
				DestTime:     d.at,
				DestDupIndex: j + 1,
				DestDupCount: len(destRows),
			})
		}
	}
}

// keyedRow is a row with its key already derived and its event time already
// parsed, so grouping and ordering never re-evaluate CEL.
type keyedRow struct {
	row Row
	at  *time.Time
}

func groupRowsByKey(rows []Row, keyOf KeyFunc, timeColumn string) (map[string][]keyedRow, error) {
	groups := make(map[string][]keyedRow)
	for index, row := range rows {
		key, err := keyOf(row)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", index, err)
		}
		at, err := rowTime(row, timeColumn)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", index, err)
		}
		groups[key] = append(groups[key], keyedRow{row: row, at: at})
	}
	// Order each group oldest-first so the cartesian expansion pairs the first
	// source attempt with the first destination arrival.
	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			switch {
			case group[i].at == nil:
				return false
			case group[j].at == nil:
				return true
			default:
				return group[i].at.Before(*group[j].at)
			}
		})
	}
	return groups, nil
}

func unionKeys(a, b map[string][]keyedRow) []string {
	keys := make([]string, 0, len(a)+len(b))
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, both := a[key]; !both {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// timeColumnOf resolves which row key holds the event time. An explicit column
// must exist on the rows; a discovered one is best-effort, because plenty of
// profiles legitimately have no timestamp at all.
func timeColumnOf(explicit string, profile Profile, rows []Row) (string, error) {
	if explicit != "" {
		if len(rows) == 0 {
			return explicit, nil
		}
		for _, row := range rows {
			if _, ok := row[explicit]; ok {
				return explicit, nil
			}
		}
		return "", fmt.Errorf("timeColumn %q is not present on any row", explicit)
	}
	for _, column := range profile.Columns {
		if column.Kind == ColumnKindTimestamp {
			return column.Name, nil
		}
	}
	return "", nil
}

func rowTime(row Row, column string) (*time.Time, error) {
	if column == "" {
		return nil, nil
	}
	value, ok := row[column]
	if !ok || value == nil {
		return nil, nil
	}
	at, err := coerceTime(value)
	if err != nil {
		return nil, fmt.Errorf("column %q: %w", column, err)
	}
	if at == nil || at.IsZero() {
		return nil, nil
	}
	return at, nil
}

// coerceTime accepts the shapes providers actually return for a timestamp: a
// real time.Time from a SQL driver, an RFC3339 string from JSON, or epoch
// seconds/millis/micros/nanos from a metrics or log backend.
func coerceTime(value any) (*time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return &typed, nil
	case *time.Time:
		return typed, nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return &parsed, nil
			}
		}
		if epoch, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return epochTime(epoch), nil
		}
		return nil, fmt.Errorf("cannot parse %q as a timestamp", typed)
	case int:
		return epochTime(int64(typed)), nil
	case int64:
		return epochTime(typed), nil
	case float64:
		return epochTime(int64(typed)), nil
	default:
		return nil, fmt.Errorf("cannot parse %T as a timestamp", value)
	}
}

// epochTime picks the epoch unit from the magnitude. The thresholds are the
// epoch value of 2001-09-09 in each unit, so any timestamp after that date is
// classified correctly and anything before it is far outside the range these
// backends emit.
func epochTime(value int64) *time.Time {
	var at time.Time
	switch {
	case value >= 1e18:
		at = time.Unix(0, value)
	case value >= 1e15:
		at = time.UnixMicro(value)
	case value >= 1e12:
		at = time.UnixMilli(value)
	default:
		at = time.Unix(value, 0)
	}
	at = at.UTC()
	return &at
}

func timeDiff(source, dest *time.Time) *time.Duration {
	if source == nil || dest == nil {
		return nil
	}
	diff := dest.Sub(*source)
	return &diff
}
