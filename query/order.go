package query

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// OrderBy is one column of a Profile's result order.
type OrderBy struct {
	// Column is the row key to order by.
	Column string `json:"column" yaml:"column"`

	// Desc orders this column descending.
	Desc bool `json:"desc,omitempty" yaml:"desc,omitempty"`

	// Unique asserts this column breaks all remaining ties, which is what makes
	// the order total. Paging requires the order to end in one.
	//
	// We validate that the author asserted a tiebreaker, not that the assertion
	// is true: whether a column is unique is a fact about their schema, not
	// about anything reachable from here. An untrue assertion produces the same
	// unstable paging an undeclared order does — but it does so having been
	// claimed, which is the difference between a bug and a silent default.
	Unique bool `json:"unique,omitempty" yaml:"unique,omitempty"`
}

// Order is a Profile's declared result order.
//
// It exists because paging of either kind is meaningless without one. An offset
// names a position in a sequence, and a cursor names a row to resume after;
// neither is well defined when consecutive executions may return rows in
// different orders, which is what both backends are free to do when nothing
// asks them otherwise.
type Order []OrderBy

// Validate rejects an order that cannot be applied.
func (o Order) Validate() error {
	seen := make(map[string]bool, len(o))
	for index, by := range o {
		if strings.TrimSpace(by.Column) == "" {
			return fmt.Errorf("order[%d]: column is required", index)
		}
		if seen[by.Column] {
			return fmt.Errorf("order[%d]: column %q is ordered twice", index, by.Column)
		}
		seen[by.Column] = true

		// A column that breaks all ties leaves nothing for a later column to
		// order, so anything after it is a misunderstanding of what was
		// declared rather than a harmless extra.
		if by.Unique && index != len(o)-1 {
			return fmt.Errorf(
				"order[%d]: column %q is marked unique but is not last; columns after a unique one can never affect the order",
				index, by.Column)
		}
	}
	return nil
}

// Pageable reports whether this order is total, and therefore whether a page of
// it can be identified twice running. The error says what to declare, because
// the fix is always an authoring one.
func (o Order) Pageable() error {
	if len(o) == 0 {
		return fmt.Errorf("no order is declared, so a page has no stable meaning; declare `order:` ending in a unique column")
	}
	if last := o[len(o)-1]; !last.Unique {
		return fmt.Errorf(
			"order ends in %q, which is not declared unique, so rows tied on it may page in any order; mark the tiebreaking column `unique: true`",
			last.Column)
	}
	return nil
}

// Columns returns the ordered column names.
func (o Order) Columns() []string {
	columns := make([]string, 0, len(o))
	for _, by := range o {
		columns = append(columns, by.Column)
	}
	return columns
}

// Fingerprint identifies this order for cursor validation. A cursor holds the
// sort values of the row it resumes after, so it is only meaningful against the
// order that produced them — replaying it against a different one would resume
// from a position that never existed.
func (o Order) Fingerprint() string {
	var canonical strings.Builder
	for _, by := range o {
		canonical.WriteString(by.Column)
		if by.Desc {
			canonical.WriteString(" desc")
		} else {
			canonical.WriteString(" asc")
		}
		canonical.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:8])
}
