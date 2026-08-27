package query

import (
	"fmt"
	"slices"
	"strings"
)

// SortBinding is one column a request may order a Profile by: the public name a
// caller sends, and the backend field it is applied to.
type SortBinding struct {
	// Column is the public column name, which is what the client sorts by.
	Column string

	// Field is the backend field the order is compiled against.
	Field string
}

// SortBindings are the columns a request may order this Profile by.
//
// Sortability asks the same question filtering does — does this column address a
// real backend field, or is its value only known once the row has been read —
// so it is answered by the same target inference, and the two can never point at
// different fields.
//
// It deliberately does not ask the filter's other questions. A column whose
// filter is disabled, or whose filter kind resolves to none, still names a field
// that can be ordered by; and a timestamp column served by the date-range
// control is excluded from the filter bindings while remaining the column most
// worth sorting on.
func (p Profile) SortBindings() ([]SortBinding, error) {
	if !SupportsRequestSort(p.Provider.Type) {
		return nil, nil
	}
	bindings := make([]SortBinding, 0, len(p.Columns))
	for _, column := range p.Columns {
		if column.Hidden {
			continue
		}
		target, declared, ok, err := columnFilterTarget(column)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		nested, _, nestable, err := resolveColumnNesting(column, target)
		if err != nil {
			return nil, err
		}
		// An order is over rows, and a container entry is not one. Sorting a
		// document by one member of a repeated field has no answer the backend
		// could give, so such a column offers no sort rather than a wrong one.
		if !nestable || nested != "" || len(target.Where) > 0 {
			continue
		}
		field, addressable, err := resolveBackendField(p, column, target, declared, nested)
		if err != nil {
			return nil, err
		}
		if !addressable {
			continue
		}
		bindings = append(bindings, SortBinding{Column: column.Name, Field: field})
	}
	return bindings, nil
}

// ColumnSortKeys maps each sortable column to the public name a request orders
// it by. It is the sort counterpart of ColumnFilterKeys, and the two are shaped
// alike so a renderer reads both the same way.
func (p Profile) ColumnSortKeys() (map[string]string, error) {
	bindings, err := p.SortBindings()
	if err != nil {
		return nil, err
	}
	keys := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		keys[binding.Column] = binding.Column
	}
	return keys, nil
}

// RequestedOrder is the order this Profile's rows are returned in when a request
// names a sort column, or its effective order when none is named.
//
// The requested column leads the declared order rather than replacing it. That
// is not a courtesy to the author: Order.Pageable requires the order to end in a
// column declared unique, so replacing it would cost every profile the ability
// to serve a second page the moment anyone sorted. Leading it keeps the
// tiebreaker where it has to be and leaves the sort meaning what the caller
// asked.
//
// A cursor is scoped by the order it was cut from, so changing the sort stales
// the cursors minted under the previous one. That is the honest outcome: the
// position they name does not exist in the new sequence.
func (p Profile) RequestedOrder(sort string, desc bool) (Order, error) {
	base, err := p.EffectiveOrder()
	if err != nil {
		return nil, err
	}
	sort = strings.TrimSpace(sort)
	if sort == "" {
		return base, nil
	}
	bindings, err := p.SortBindings()
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(bindings, func(binding SortBinding) bool { return binding.Column == sort })
	if index < 0 {
		// The profile names itself wherever this is reported, so it is not
		// repeated here.
		if len(bindings) == 0 {
			return nil, fmt.Errorf("cannot be sorted by %q: it offers no sortable columns", sort)
		}
		columns := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			columns = append(columns, binding.Column)
		}
		return nil, fmt.Errorf(
			"cannot be sorted by %q; sortable columns are %s", sort, strings.Join(columns, ", "))
	}

	requested := OrderBy{Column: bindings[index].Field, Desc: desc}
	rest := make(Order, 0, len(base))
	for _, by := range base {
		if by.Column == requested.Column {
			// The declared order already names this column. Its uniqueness is a
			// fact about the schema rather than about where it sits, so it
			// travels with the column to its new position.
			requested.Unique = by.Unique
			continue
		}
		rest = append(rest, by)
	}
	// A column that breaks all ties leaves nothing for a later column to order,
	// so sorting by the tiebreaker is sorting by it alone.
	if requested.Unique {
		return Order{requested}, nil
	}
	return append(Order{requested}, rest...), nil
}
