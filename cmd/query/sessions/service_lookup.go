package sessions

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/flanksource/clicky/formatters"

	"github.com/flanksource/commons-db/query"
)

// sessionLabelFilters are the label dimensions every lookup counts; a label a
// request filters on is counted too.
var sessionLabelFilters = []string{"target", "origin", "via", "runId", "plan", "step", "environment"}

type sessionLookupResponse struct {
	Filters map[string]sessionLookupFilter `json:"filters"`
}

type sessionLookupFilter struct {
	Label   string                           `json:"label"`
	Type    string                           `json:"type"`
	Multi   bool                             `json:"multi"`
	Options map[string]formatters.ClickyNode `json:"options"`
	Counts  map[string]int                   `json:"counts"`
}

// sessionFacet is one filter a lookup counts values of.
type sessionFacet struct {
	name, label string
	single      bool // one typed value (restartOf) rather than a multi-select
	value       func(query.SessionRecord) string
	without     func(query.SessionFilter) query.SessionFilter
}

func sessionFacets(filter query.SessionFilter) []sessionFacet {
	field := func(name, label string, value func(query.SessionRecord) string, clear func(*query.SessionFilter)) sessionFacet {
		return sessionFacet{name: name, label: label, value: value, without: func(f query.SessionFilter) query.SessionFilter {
			clear(&f)
			return f
		}}
	}
	facets := []sessionFacet{
		field("profile", "Profile", func(r query.SessionRecord) string { return r.Profile }, func(f *query.SessionFilter) { f.Profile = nil }),
		field("kind", "Kind", func(r query.SessionRecord) string { return string(r.Kind) }, func(f *query.SessionFilter) { f.Kind = nil }),
		field("state", "State", func(r query.SessionRecord) string { return string(r.State) }, func(f *query.SessionFilter) { f.State = nil }),
		field("principal", "Principal", func(r query.SessionRecord) string { return r.Principal }, func(f *query.SessionFilter) { f.Principal = nil }),
		field("restartOf", "Restart of", func(r query.SessionRecord) string { return r.RestartOf }, func(f *query.SessionFilter) { f.RestartOf = nil }),
	}
	facets[len(facets)-1].single = true
	keys := slices.Clone(sessionLabelFilters)
	for key := range filter.Labels {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		facets = append(facets, sessionFacet{
			name: "label." + key, label: key,
			value: func(r query.SessionRecord) string { return r.Labels[key] },
			without: func(f query.SessionFilter) query.SessionFilter {
				labels := map[string][]string{}
				for k, v := range f.Labels {
					if k != key {
						labels[k] = v
					}
				}
				f.Labels = labels
				return f
			},
		})
	}
	return facets
}

// lookup counts each facet's values over the authorized sessions in the window
// that match every other active filter, so a count is what selecting the
// value would list.
func (h *sessionHandler) lookup(w http.ResponseWriter, r *http.Request, filter query.SessionFilter) {
	only, search := r.URL.Query().Get("__lookup_filter"), strings.ToLower(r.URL.Query().Get("__lookup_q"))
	windowed := query.SessionFilter{Role: filter.Role, From: filter.From, To: filter.To, Allow: filter.Allow}
	candidates, _, err := h.candidates(r.Context(), windowed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := sessionLookupResponse{Filters: map[string]sessionLookupFilter{}}
	for _, facet := range sessionFacets(filter) {
		if only != "" && facet.name != only {
			continue
		}
		counted := facet.without(filter)
		counted.Limit, counted.Offset = 0, 0
		matches, err := query.ApplySessionFilter(candidates, counted)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response.Filters[facet.name] = countFacet(facet, matches.Items, search)
	}
	if only != "" && len(response.Filters) == 0 {
		http.Error(w, fmt.Sprintf("session list has no lookup filter %q", only), http.StatusBadRequest)
		return
	}
	writeSessionJSON(w, http.StatusOK, response)
}

func countFacet(facet sessionFacet, records []query.SessionRecord, search string) sessionLookupFilter {
	filter := sessionLookupFilter{
		Label: facet.label, Type: "multi-filter", Multi: !facet.single,
		Options: map[string]formatters.ClickyNode{}, Counts: map[string]int{},
	}
	if facet.single {
		filter.Type = "value"
	}
	for _, rec := range records {
		value := facet.value(rec)
		if value == "" || (search != "" && !strings.Contains(strings.ToLower(value), search)) {
			continue
		}
		filter.Options[value] = formatters.ClickyNode{Kind: "text", Text: value, Plain: value}
		filter.Counts[value]++
	}
	return filter
}
