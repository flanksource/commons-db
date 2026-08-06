package profiles

import (
	"context"
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query"
)

// profileFilter makes the profile entity a lookup target, so a form field tagged
// with x-clicky-lookup (imports, reconcile.dest) renders a searchable profile
// picker instead of a free-text box that silently accepts a typo.
//
// Both the option key and its label are the profile's own name, because that is
// exactly what those fields store. The name also carries the hierarchy the
// picker browses (`jms.incoming`), which is why the label must not be decorated.
type profileFilter struct{ service *Service }

func (profileFilter) Key() string   { return "profile" }
func (profileFilter) Label() string { return "Profile" }

// Lookup resolves the currently-selected values. The picker pins its own current
// value client-side, so the lookup response needs no server-resolved selection.
func (profileFilter) Lookup(*profileListOpts) (map[string]api.Textable, error) { return nil, nil }

// Options enumerates every profile (the non-search path).
func (f profileFilter) Options(opts profileListOpts) map[string]api.Textable {
	options, _ := f.OptionsWithQuery(opts, "", 0)
	return options
}

// OptionsWithQuery lists profiles matching the name substring query. total is the
// full match count so the UI can show "… and N more" past the head limit.
func (f profileFilter) OptionsWithQuery(_ profileListOpts, query string, limit int) (map[string]api.Textable, int) {
	store, err := f.service.store()
	if err != nil {
		return nil, 0
	}
	profiles, err := store.List(context.Background())
	if err != nil {
		return nil, 0
	}
	return profileOptions(profiles, query, limit)
}

// profileOptions builds the lookup option set from already-listed profiles:
// <name> => <name>, filtered by the name substring query and capped at limit
// (limit <= 0 means no cap). total is the full match count before the cap. Pure,
// so it is unit-testable without a store.
func profileOptions(profiles []query.Profile, query string, limit int) (map[string]api.Textable, int) {
	q := strings.ToLower(strings.TrimSpace(query))
	options := make(map[string]api.Textable, len(profiles))
	total := 0
	for _, profile := range profiles {
		if q != "" && !strings.Contains(strings.ToLower(profile.Name), q) {
			continue
		}
		total++
		if limit > 0 && total > limit {
			continue
		}
		options[profile.Name] = api.Text{Content: profile.Name}
	}
	return options, total
}
