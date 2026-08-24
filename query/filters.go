package query

import (
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/context"
)

// FilterDef is a named row predicate evaluated after aliases. Profile authors
// use it to trim known noise — access logs, health checks, per-second
// heartbeats — without wrapping every column in a conditional.
//
// A filter matches when every entry in Fields evaluates to true. Exclude
// inverts what a match means: false (the default) keeps matching rows and drops
// the rest, true drops matching rows.
//
// Hidden decides whether the filter applies at all. Hidden filters are
// always-on server-side noise suppression. A non-hidden filter is a declared
// quick filter for a UI to offer as a toggle: it is carried on the profile and
// never applied here, because applying one unconditionally would hide useful
// default output — which is what a predicate like `row.level == "ERROR"` would
// do to every INFO line.
type FilterDef struct {
	// Name identifies the filter in the UI and in error messages. Required for a
	// quick filter, which a UI has to be able to select by name.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description surfaces in the UI tooltip and listing.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Fields holds CEL predicates keyed by a label, AND-ed together. Each is
	// evaluated with the row bound as `row`.
	Fields map[string]string `json:"fields,omitempty" yaml:"fields,omitempty"`

	// Exclude inverts the match: matching rows are dropped rather than kept.
	Exclude bool `json:"exclude,omitempty" yaml:"exclude,omitempty"`

	// Hidden marks the filter always-on rather than togglable.
	Hidden bool `json:"hidden,omitempty" yaml:"hidden,omitempty"`
}

// validate reports a filter that cannot do anything. An empty Fields map would
// match every row vacuously, which reads as "no filter" but renders as either
// "keep everything" or "drop everything" depending on Exclude — too sharp a
// difference to infer from an omission.
func (f FilterDef) validate(index int) error {
	if len(f.Fields) == 0 {
		return fmt.Errorf("filter[%d] %q: fields is required", index, f.Name)
	}
	for key, expression := range f.Fields {
		if expression == "" {
			return fmt.Errorf("filter[%d] %q: field %q has no expression", index, f.Name, key)
		}
	}
	if !f.Hidden && f.Name == "" {
		return fmt.Errorf("filter[%d]: a quick filter needs a name for a UI to offer it by", index)
	}
	return nil
}

// activeFilters returns the filters that actually run — the hidden ones. Quick
// filters are validated by Profile.Validate but never applied here.
func activeFilters(filters []FilterDef) ([]FilterDef, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	out := make([]FilterDef, 0, len(filters))
	for index, filter := range filters {
		if !filter.Hidden {
			continue
		}
		if err := filter.validate(index); err != nil {
			return nil, err
		}
		out = append(out, filter)
	}
	return out, nil
}

// keepRow reports whether the row survives every filter.
//
// Predicates are evaluated with evalRowCEL, the same environment aliases and
// columns get, so a filter can name a field exactly as the column beside it
// does — bare `logger`, or `row.logger`, or `span.logger`.
//
// A predicate that errors, or returns a non-boolean, is a failure rather than a
// silent non-match: a wrong expression would otherwise drop or keep the whole
// result with nothing said, and "the query returned nothing" is
// indistinguishable from "the backend had nothing".
func keepRow(ctx context.Context, filters []FilterDef, row Row) (bool, error) {
	for _, filter := range filters {
		matched := true
		for _, key := range sortedFieldKeys(filter.Fields) {
			value, err := evalRowCEL(ctx, filter.Fields[key], row)
			if err != nil {
				return false, fmt.Errorf("filter %q: field %q: %w", filter.Name, key, err)
			}
			predicate, ok := value.(bool)
			if !ok {
				return false, fmt.Errorf("filter %q: field %q: expected a boolean, got %T (%v)",
					filter.Name, key, value, value)
			}
			if !predicate {
				matched = false
				break
			}
		}
		if matched == filter.Exclude {
			return false, nil
		}
	}
	return true, nil
}

// sortedFieldKeys fixes the evaluation order so a filter with two bad fields
// always reports the same one.
func sortedFieldKeys(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
