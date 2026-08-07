package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Cursor is a position in an ordered result set, resumed from by the request
// that follows.
//
// It is opaque on purpose. A caller able to read a cursor is a caller able to
// build one, and a hand-built position is wrong in the one way this package
// cannot tolerate: quietly. Keeping it closed means the only positions in
// circulation are ones this package issued, so the checks below are checks on
// data that was once true rather than on a stranger's arithmetic.
type Cursor string

// IsZero reports whether c names no position — the start of the result set.
func (c Cursor) IsZero() bool { return c == "" }

// cursorVersion is stamped into every cursor so a format change invalidates
// outstanding cursors loudly rather than decoding them into wrong positions.
const cursorVersion = 1

// ErrCursorStale is returned when a cursor is replayed against a query it did
// not come from — a changed filter, param or order. The position it holds is
// real but no longer locatable, so it is refused rather than served: resuming
// "after" a row this query never produces silently skips or repeats an
// unknowable number of rows.
var ErrCursorStale = errors.New("cursor no longer matches this query")

// cursorPayload is a Cursor's decoded form.
type cursorPayload struct {
	Version int    `json:"v"`
	Profile string `json:"n"`
	Order   string `json:"o"`
	Filters string `json:"f"`
	Keys    []any  `json:"k"`
	PIT     string `json:"p,omitempty"`
}

// CursorPosition is a validated cursor: where to resume, and the point-in-time
// the walk is pinned to when the backend supports one.
type CursorPosition struct {
	// Keys are the ordered sort values of the last row of the previous page.
	// A provider resumes strictly after them.
	Keys []any

	// PIT identifies the backend snapshot this walk is reading, when it has
	// one. Empty means the walk sees the index as it changes.
	PIT string
}

// IsZero reports whether this position names the start of the result set.
func (p CursorPosition) IsZero() bool { return len(p.Keys) == 0 }

// CursorScope is everything a cursor is valid against: the query that issued
// it, the order it was cut from, and the inputs deciding which rows exist.
//
// It is the resolved inputs rather than the built ProviderRequest because a
// keyset profile templates the cursor's own keys into its query text — so the
// request a cursor is checked against is one the cursor helped build, and
// checking against it would be circular.
type CursorScope struct {
	Profile string
	Order   Order
	Params  map[string]any
	Roles   map[string]ParamRole
	Filters []ColumnFilterValue
}

// EncodeCursor issues a cursor resuming after keys, which must be the sort
// values of the last row of the page being returned, in the order's column
// order.
func EncodeCursor(scope CursorScope, keys []any, pit string) (Cursor, error) {
	if err := scope.Order.Pageable(); err != nil {
		return "", err
	}
	if len(keys) != len(scope.Order) {
		return "", fmt.Errorf("cursor needs one key per order column: order has %d, got %d", len(scope.Order), len(keys))
	}
	filters, err := scope.fingerprint()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursorPayload{
		Version: cursorVersion,
		Profile: scope.Profile,
		Order:   scope.Order.Fingerprint(),
		Filters: filters,
		Keys:    keys,
		PIT:     pit,
	})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(encoded)), nil
}

// DecodeCursor validates c against the query it is being replayed on and
// returns the position to resume from. Every mismatch is ErrCursorStale: from
// the caller's side a forged cursor and an outdated one are the same event —
// this request cannot honour this position — and both must be refused.
func DecodeCursor(c Cursor, scope CursorScope) (CursorPosition, error) {
	if c.IsZero() {
		return CursorPosition{}, nil
	}
	if err := scope.Order.Pageable(); err != nil {
		return CursorPosition{}, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil {
		return CursorPosition{}, fmt.Errorf("%w: it is not a cursor this server issued", ErrCursorStale)
	}
	var payload cursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return CursorPosition{}, fmt.Errorf("%w: it is not a cursor this server issued", ErrCursorStale)
	}
	filters, err := scope.fingerprint()
	if err != nil {
		return CursorPosition{}, err
	}
	switch {
	case payload.Version != cursorVersion:
		return CursorPosition{}, fmt.Errorf("%w: it was issued by an older version of this server", ErrCursorStale)
	case payload.Profile != scope.Profile:
		return CursorPosition{}, fmt.Errorf("%w: it was issued for profile %q", ErrCursorStale, payload.Profile)
	case payload.Order != scope.Order.Fingerprint():
		return CursorPosition{}, fmt.Errorf("%w: the sort order changed, so its position no longer exists", ErrCursorStale)
	case payload.Filters != filters:
		return CursorPosition{}, fmt.Errorf("%w: the filters changed, so its position no longer exists", ErrCursorStale)
	case len(payload.Keys) != len(scope.Order):
		return CursorPosition{}, fmt.Errorf("%w: it carries %d keys for a %d column order", ErrCursorStale, len(payload.Keys), len(scope.Order))
	}
	return CursorPosition{Keys: payload.Keys, PIT: payload.PIT}, nil
}

// CursorParams exposes a decoded position under the profile's cursor-role
// parameter, so a keyset query can template its own resume predicate:
//
//	SELECT created_at, id FROM events
//	{{if .params.cursor}}
//	WHERE (created_at, id) > ({{.params.cursor.created_at}}, {{.params.cursor.id}})
//	{{end}}
//	ORDER BY created_at, id
//
// The guard is not optional: the first page of a walk carries no position, and
// this returns nil for it. A predicate written without the guard renders
// against nothing on exactly the request that starts the walk.
//
// The values are keyed by order column, which is the vocabulary the author
// already wrote the order in.
func (p CursorPosition) CursorParams(order Order) map[string]any {
	if p.IsZero() {
		return nil
	}
	values := make(map[string]any, len(order))
	for index, by := range order {
		values[by.Column] = p.Keys[index]
	}
	return values
}

// fingerprint identifies the result set a cursor was cut from: the params and
// filters that decide which rows exist, and nothing that decides only how many
// are returned at a time.
//
// Paging params are excluded deliberately. Changing the page size mid-walk
// narrows or widens the next page but does not move the position a cursor
// names, so invalidating on it would refuse a request that is perfectly
// answerable. Changing a filter does move it, which is why that invalidates.
func (s CursorScope) fingerprint() (string, error) {
	params := make(map[string]any, len(s.Params))
	for name, value := range s.Params {
		switch s.Roles[name] {
		case ParamRoleLimit, ParamRoleOffset, ParamRoleCursor:
			continue
		}
		params[name] = value
	}

	// Filters are canonicalised rather than hashed as given: the same filter
	// set resolved twice must fingerprint the same, and slice order is not part
	// of what the filters mean.
	filters := make([]string, 0, len(s.Filters))
	for _, filter := range s.Filters {
		encoded, err := json.Marshal(filter)
		if err != nil {
			return "", fmt.Errorf("cannot fingerprint filter %q for cursor validation: %w", filter.Key, err)
		}
		filters = append(filters, string(encoded))
	}
	sort.Strings(filters)

	encoded, err := json.Marshal(struct {
		Params  any      `json:"p"`
		Filters []string `json:"f"`
	}{Params: params, Filters: filters})
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint params for cursor validation: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8]), nil
}
