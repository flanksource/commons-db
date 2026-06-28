package main

import (
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/models"
)

// connectionFilter makes the connection entity a lookup target so a form field
// tagged with x-clicky-lookup can render a searchable connection picker. Option
// keys are connection://<name> references (the value a Profile stores in
// provider.connection); the label shows the name and type. Server-side search
// matches the name substring, and the picker scopes the list to the connection
// types valid for the selected provider via the `types` flag (connListOpts.Types).
type connectionFilter struct{}

func (connectionFilter) Key() string   { return "connection" }
func (connectionFilter) Label() string { return "Connection" }

// Lookup resolves the currently-selected values. The picker pins its own current
// value client-side, so the lookup response needs no server-resolved selection.
func (connectionFilter) Lookup(*connListOpts) (map[string]api.Textable, error) { return nil, nil }

// Options enumerates every in-scope connection (the non-search path).
func (f connectionFilter) Options(opts connListOpts) map[string]api.Textable {
	options, _ := f.OptionsWithQuery(opts, "", 0)
	return options
}

// OptionsWithQuery lists connections scoped to opts (type/types) and matching the
// name substring query, returning connection://<name> => "<name> (<type>)". total
// is the full match count so the UI can show "… and N more" past the head limit.
func (connectionFilter) OptionsWithQuery(opts connListOpts, query string, limit int) (map[string]api.Textable, int) {
	db, err := currentDB()
	if err != nil {
		return nil, 0
	}
	conns, err := listConnections(db, opts)
	if err != nil {
		return nil, 0
	}
	return connectionOptions(conns, query, limit)
}

// connectionOptions builds the lookup option set from already-listed connections:
// connection://<name> => "<name> (<type>)", filtered by the name substring query
// and capped at limit (limit <= 0 means no cap). total is the full match count
// before the cap, so the UI can render "… and N more". Type scoping is applied
// upstream by listConnections; this is pure so it is unit-testable without a DB.
func connectionOptions(conns []*models.Connection, query string, limit int) (map[string]api.Textable, int) {
	q := strings.ToLower(strings.TrimSpace(query))
	options := make(map[string]api.Textable, len(conns))
	total := 0
	for _, c := range conns {
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
			continue
		}
		total++
		if limit > 0 && total > limit {
			continue
		}
		label := api.Text{Content: c.Name}
		if c.Type != "" {
			label = label.AddText(" ("+c.Type+")", "text-muted-foreground")
		}
		options["connection://"+c.Name] = label
	}
	return options, total
}
