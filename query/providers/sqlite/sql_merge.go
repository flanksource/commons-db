package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

const defaultMergeConcurrency = 8

// SQLMember is one partition of a merged read: a client on one of several
// databases sharing a schema.
type SQLMember struct {
	// Name identifies the member in the cursor and breaks ties between members
	// holding rows with equal keys, so it must be stable across requests.
	Name   string
	Client *sql.DB
	// Labels become constant columns of every row this member returns, so
	// filters, order and projections can address them like stored columns.
	Labels map[string]string
	// ReadPage reads a member through its native profile. When supplied, Client
	// is unused; rows must expose the common order keys declared by Read.Order.
	ReadPage func(context.Context, SQLPageRequest) (SQLPageResult, error)
}

// labelledQuery wraps statement so each label is a column of its rows. The
// values are SQL literals rather than binds: a bind would have to precede the
// author's own positional arguments, which it textually follows.
func labelledQuery(statement string, labels map[string]string) string {
	if len(labels) == 0 {
		return statement
	}
	projection := `"__cdb_partition".*`
	for _, column := range labelColumns(labels) {
		projection += fmt.Sprintf(", '%s' AS %s", strings.ReplaceAll(labels[column], "'", "''"), quote(column))
	}
	return fmt.Sprintf(`SELECT %s FROM (%s) AS "__cdb_partition"`, projection, statement)
}

func quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// SQLMergeRequest reads Read from every member and merges the rows into one
// ordered result.
type SQLMergeRequest struct {
	Driver  string
	Members []SQLMember
	// Read is the request every member runs. Its Position is the merged
	// cursor: the per-member positions travel in Position.State under
	// query.ProviderStateKey.
	Read SQLPageRequest
	// Concurrency bounds the members read at once; zero means 8.
	Concurrency int
}

// mergeMember is one member's progress through a merge.
type mergeMember struct {
	SQLMember
	buffer []query.Row
	// head is the order keys of buffer[0].
	head []any
	// keys is the order keys of the last row this member gave the merge;
	// nil before its first.
	keys []any
	// more says the member has rows beyond those already fetched.
	more  bool
	total *query.Total
}

// mergeState is one member's position as the cursor carries it.
type mergeState struct {
	Keys json.RawMessage `json:"k,omitempty"`
	Done bool            `json:"d,omitempty"`
}

// MergeSQLPages is a k-way merge of the same paged read over several databases.
//
// Every member reads with ReadSQLPage under the same filters and order, each
// resuming from its own last row, and the merge emits the smallest head row
// across members until the page is full. Rows whose keys tie across members
// — the order's unique column is only unique within one database — are broken
// by member name, and because each member resumes from its own position a
// resumed walk neither repeats nor skips them.
//
// Only cursor paging is supported: an offset into a merge would have to read
// and discard every row before it from every member.
func MergeSQLPages(ctx context.Context, request SQLMergeRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		members, err := request.start()
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		page := request.Read.Page
		remaining := page.Ceiling
		first := request.Read.Position.IsZero()
		for {
			limit := page.Limit
			if remaining > 0 && remaining < limit {
				limit = remaining
			}
			rows, err := request.mergeRows(ctx, members, limit)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			more := hasMore(members)
			capped := remaining > 0 && len(rows) >= remaining && more
			result := query.Page{Rows: rows, HasMore: more && !capped, Truncated: capped}
			if first && !page.SkipTotal {
				result.Total = mergedTotal(members)
			}
			first = false
			if result.HasMore {
				if result.NextKeys, err = providerkit.OrderKeys(rows[len(rows)-1], request.Read.Order); err != nil {
					yield(query.Page{}, err)
					return
				}
				if result.State, err = encodeMergeState(members); err != nil {
					yield(query.Page{}, err)
					return
				}
			}
			if !yield(result, nil) || !result.HasMore {
				return
			}
			if remaining > 0 {
				remaining -= len(rows)
			}
		}
	}
}

func (r SQLMergeRequest) start() ([]*mergeMember, error) {
	if len(r.Members) == 0 {
		return nil, fmt.Errorf("a merged sql read needs at least one member")
	}
	if err := r.Read.Order.Pageable(); err != nil {
		return nil, fmt.Errorf("a merged sql read pages by cursor only: %w", err)
	}
	if r.Read.Page.Offset > 0 {
		return nil, fmt.Errorf("a merged sql read pages by cursor only; offset %d cannot be served", r.Read.Page.Offset)
	}
	if err := r.Read.Page.Validate(); err != nil {
		return nil, err
	}
	states, err := decodeMergeState(r.Read.Position)
	if err != nil {
		return nil, err
	}
	members := make([]*mergeMember, 0, len(r.Members))
	seen := make(map[string]bool, len(r.Members))
	for _, member := range r.Members {
		if member.Name == "" || seen[member.Name] {
			return nil, fmt.Errorf("merged sql members need unique, non-empty names; got %q twice or empty", member.Name)
		}
		seen[member.Name] = true
		progress := &mergeMember{SQLMember: member, more: true}
		if states != nil {
			state, ok := states[member.Name]
			if !ok {
				return nil, fmt.Errorf("%w: member %q was not part of the read the cursor came from", query.ErrCursorStale, member.Name)
			}
			if progress.keys, err = decodeMemberKeys(state); err != nil {
				return nil, err
			}
			progress.more = !state.Done
		}
		members = append(members, progress)
	}
	if states != nil && len(states) != len(members) {
		return nil, fmt.Errorf("%w: the cursor names %d members, the read has %d", query.ErrCursorStale, len(states), len(members))
	}
	return members, nil
}

// mergeRows emits up to limit rows, refilling a member whenever its buffer runs
// dry while it still has rows: its next row may be the smallest of all.
func (r SQLMergeRequest) mergeRows(ctx context.Context, members []*mergeMember, limit int) ([]query.Row, error) {
	rows := make([]query.Row, 0, limit)
	for len(rows) < limit {
		if err := r.refill(ctx, members, limit); err != nil {
			return nil, err
		}
		next, err := smallestHead(members, r.Read.Order)
		if err != nil {
			return nil, err
		}
		if next == nil {
			break
		}
		rows = append(rows, next.buffer[0])
		next.keys = next.head
		next.buffer = next.buffer[1:]
		if next.head, err = headKeys(next.buffer, r.Read.Order); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (r SQLMergeRequest) refill(ctx context.Context, members []*mergeMember, limit int) error {
	group, groupCtx := errgroup.WithContext(ctx)
	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = defaultMergeConcurrency
	}
	group.SetLimit(concurrency)
	for _, member := range members {
		if len(member.buffer) > 0 || !member.more {
			continue
		}
		group.Go(func() error {
			read := r.Read
			read.Query = labelledQuery(read.Query, member.Labels)
			read.Position = query.CursorPosition{Keys: member.keys}
			read.Page = query.PageRequest{Limit: limit, SkipTotal: r.Read.Page.SkipTotal, Strategy: query.PagingCursor}
			reader := member.ReadPage
			if reader == nil {
				reader = func(ctx context.Context, request SQLPageRequest) (SQLPageResult, error) {
					return ReadSQLPage(ctx, member.Client, r.Driver, request)
				}
			}
			result, err := reader(groupCtx, read)
			if err != nil {
				return fmt.Errorf("partition %q: %w", member.Name, err)
			}
			if member.keys == nil {
				member.total = result.Total
			}
			member.buffer, member.more = result.Rows, result.HasMore
			member.head, err = headKeys(member.buffer, r.Read.Order)
			if err != nil {
				return fmt.Errorf("partition %q: %w", member.Name, err)
			}
			return nil
		})
	}
	return group.Wait()
}

func smallestHead(members []*mergeMember, order query.Order) (*mergeMember, error) {
	var smallest *mergeMember
	for _, member := range members {
		if len(member.buffer) == 0 {
			continue
		}
		if smallest == nil {
			smallest = member
			continue
		}
		compared, err := compareOrderKeys(member.head, smallest.head, order)
		if err != nil {
			return nil, err
		}
		if compared < 0 || (compared == 0 && member.Name < smallest.Name) {
			smallest = member
		}
	}
	return smallest, nil
}

func headKeys(buffer []query.Row, order query.Order) ([]any, error) {
	if len(buffer) == 0 {
		return nil, nil
	}
	return providerkit.OrderKeys(buffer[0], order)
}

func hasMore(members []*mergeMember) bool {
	for _, member := range members {
		if len(member.buffer) > 0 || member.more {
			return true
		}
	}
	return false
}

// mergedTotal sums the members' totals: they are disjoint databases, so the
// whole is their sum. It is unknown when any member's is.
func mergedTotal(members []*mergeMember) *query.Total {
	merged := &query.Total{Exact: true}
	for _, member := range members {
		if member.total == nil {
			return nil
		}
		merged.Value += member.total.Value
		merged.Exact = merged.Exact && member.total.Exact
	}
	return merged
}

func encodeMergeState(members []*mergeMember) (map[string][]byte, error) {
	states := make(map[string]mergeState, len(members))
	for _, member := range members {
		state := mergeState{Done: len(member.buffer) == 0 && !member.more}
		if member.keys != nil {
			keys, err := query.MarshalCursorKeys(member.keys)
			if err != nil {
				return nil, fmt.Errorf("partition %q: %w", member.Name, err)
			}
			state.Keys = keys
		}
		states[member.Name] = state
	}
	encoded, err := json.Marshal(states)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{query.ProviderStateKey: encoded}, nil
}

func decodeMergeState(position query.CursorPosition) (map[string]mergeState, error) {
	if position.IsZero() {
		return nil, nil
	}
	encoded, ok := position.State[query.ProviderStateKey]
	if !ok {
		return nil, fmt.Errorf("%w: it carries no per-partition positions, so it was not issued by a merged read", query.ErrCursorStale)
	}
	var states map[string]mergeState
	if err := json.Unmarshal(encoded, &states); err != nil {
		return nil, fmt.Errorf("%w: invalid per-partition positions: %v", query.ErrCursorStale, err)
	}
	return states, nil
}

func decodeMemberKeys(state mergeState) ([]any, error) {
	if len(state.Keys) == 0 {
		return nil, nil
	}
	return query.UnmarshalCursorKeys(state.Keys)
}
