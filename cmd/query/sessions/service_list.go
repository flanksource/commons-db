package sessions

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/datetime"
)

// Paging bounds of the session list.
const (
	defaultSessionPageLimit = 50
	maxSessionPageLimit     = 500
)

// sessionFilterParams are the list's filter parameters, besides label.<key>.
var sessionFilterParams = []string{"profile", "kind", "role", "state", "principal", "restartOf"}

// sessionListParams are every other parameter the list accepts.
var sessionListParams = []string{"from", "to", "sort", "order", "limit", "offset", "format",
	"__lookup", "__lookup_filter", "__lookup_q"}

func (h *sessionHandler) list(w http.ResponseWriter, r *http.Request) {
	filter, err := parseSessionFilter(r.URL.Query(), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	format, err := sessionListFormat(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter.Allow = h.readAllow(r)
	if r.URL.Query().Get("__lookup") == "filters" {
		h.lookup(w, r, filter)
		return
	}
	ctx := withSessionReadMemo(r.Context())
	page, err := h.listRecords(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infos, err := h.overlay(ctx, page.Items, filter.Allow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("X-Total-Count", strconv.Itoa(page.Total))
	header.Set("X-Page-Limit", strconv.Itoa(filter.Limit))
	header.Set("X-Page-Offset", strconv.Itoa(filter.Offset))
	header.Set("X-Sessions-Shared", strconv.FormatBool(page.Shared))
	header.Set("Access-Control-Expose-Headers", "X-Total-Count, X-Page-Limit, X-Page-Offset, X-Sessions-Shared")
	if format == "clicky-json" {
		writeSessionTable(w, infos)
		return
	}
	writeSessionJSON(w, http.StatusOK, sessionListResponse{Items: infos, Total: page.Total, Shared: page.Shared})
}

// sessionListFormat negotiates the list's representation as profile lists do
// (profiles.RequestedFormat): clicky-json is the table document, json the
// items. A ?format naming anything else is refused; an Accept ranking a format
// the list has no rendering for — a browser's text/html — is served json, as
// HTTP lets a server answer an Accept it cannot meet.
func sessionListFormat(r *http.Request) (string, error) {
	format := profiles.RequestedFormat(r)
	switch {
	case format == "json" || format == "clicky-json":
		return format, nil
	case r.URL.Query().Get("format") != "":
		return "", fmt.Errorf("session list format %q is not json or clicky-json", r.URL.Query().Get("format"))
	}
	return "json", nil
}

type sessionListResponse struct {
	Items  []query.SessionInfo `json:"items"`
	Total  int                 `json:"total"`
	Shared bool                `json:"shared"`
}

// listRecords is one page of the sessions the registry holds and the store
// persisted, a live session's own record replacing its stored copy. When the
// registry holds no session the filter could select, the store's own page is
// the answer, and the store pages it.
func (h *sessionHandler) listRecords(ctx context.Context, filter query.SessionFilter) (query.SessionPage, error) {
	if h.sessions != nil && !h.registryMaySelect(filter) {
		page, err := h.sessions.List(ctx, filter)
		if err != nil {
			return query.SessionPage{}, fmt.Errorf("list sessions: %w", err)
		}
		return page, nil
	}
	candidates, shared, err := h.candidates(ctx, filter)
	if err != nil {
		return query.SessionPage{}, err
	}
	page, err := query.ApplySessionFilter(candidates, filter)
	page.Shared = shared
	return page, err
}

// registryMaySelect reports whether a session the registry holds could be on
// filter's page. Only the start fields are matched: a live session's state can
// differ from its stored copy's, which it replaces.
func (h *sessionHandler) registryMaySelect(filter query.SessionFilter) bool {
	filter.State, filter.Limit, filter.Offset = nil, 0, 0
	page, err := query.ApplySessionFilter(h.liveRecords(), filter)
	if err != nil {
		// parseSessionFilter validated the filter; ApplySessionFilter fails on
		// nothing else.
		panic(fmt.Sprintf("sessions: apply a validated filter: %v", err))
	}
	return page.Total > 0
}

// candidates is every record filter selects, unpaged, live ones included.
func (h *sessionHandler) candidates(ctx context.Context, filter query.SessionFilter) ([]query.SessionRecord, bool, error) {
	live := h.liveRecords()
	if h.sessions == nil {
		return live, false, nil
	}
	filter.Limit, filter.Offset = 0, 0
	stored, err := h.sessions.List(ctx, filter)
	if err != nil {
		return nil, false, fmt.Errorf("list sessions: %w", err)
	}
	byID := make(map[string]int, len(stored.Items))
	records := stored.Items
	for i, rec := range records {
		byID[rec.ID] = i
	}
	for _, rec := range live {
		if i, ok := byID[rec.ID]; ok {
			records[i] = rec
		} else {
			records = append(records, rec)
		}
	}
	return records, stored.Shared, nil
}

// parseSessionFilter reads the list's query parameters. A filter value is a
// comma-separated list, repeated keys adding to it; role defaults to capture.
func parseSessionFilter(values url.Values, now time.Time) (query.SessionFilter, error) {
	filter := query.SessionFilter{Sort: "startedAt", Desc: true, Limit: defaultSessionPageLimit, Role: []string{string(query.SessionRoleCapture)}}
	targets := map[string]*[]string{
		"profile": &filter.Profile, "kind": &filter.Kind, "role": &filter.Role, "state": &filter.State,
		"principal": &filter.Principal, "restartOf": &filter.RestartOf,
	}
	for key, raw := range values {
		if patterns := splitFilterValues(raw); strings.HasPrefix(key, "label.") && key != "label." {
			if len(patterns) > 0 {
				if filter.Labels == nil {
					filter.Labels = map[string][]string{}
				}
				filter.Labels[strings.TrimPrefix(key, "label.")] = patterns
			}
		} else if target, ok := targets[key]; ok {
			if len(patterns) > 0 {
				*target = patterns
			}
		} else if !slices.Contains(sessionListParams, key) {
			return filter, fmt.Errorf("unknown session list parameter %q; filters are %s and label.<key>", key, strings.Join(sessionFilterParams, ", "))
		}
	}
	if err := parseSessionPaging(values, &filter, now); err != nil {
		return filter, err
	}
	return filter, filter.Validate()
}

func splitFilterValues(raw []string) []string {
	var patterns []string
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				patterns = append(patterns, part)
			}
		}
	}
	return patterns
}

func parseSessionPaging(values url.Values, filter *query.SessionFilter, now time.Time) error {
	if sort := values.Get("sort"); sort != "" {
		filter.Sort = sort
	}
	switch order := values.Get("order"); order {
	case "", "desc":
	case "asc":
		filter.Desc = false
	default:
		return fmt.Errorf("order %q must be asc or desc", order)
	}
	var err error
	if filter.Limit, err = intParam(values, "limit", defaultSessionPageLimit, 1, maxSessionPageLimit); err != nil {
		return err
	}
	if filter.Offset, err = intParam(values, "offset", 0, 0, -1); err != nil {
		return err
	}
	if filter.From, err = timeParam(values, "from", now); err != nil {
		return err
	}
	filter.To, err = timeParam(values, "to", now)
	return err
}

// intParam reads an integer param between minimum and maximum (a negative
// maximum is unbounded).
func intParam(values url.Values, key string, fallback, minimum, maximum int) (int, error) {
	raw := values.Get(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || (maximum >= 0 && value > maximum) {
		bound := fmt.Sprintf("at least %d", minimum)
		if maximum >= 0 {
			bound = fmt.Sprintf("between %d and %d", minimum, maximum)
		}
		return 0, fmt.Errorf("%s %s must be a whole number %s", key, raw, bound)
	}
	return value, nil
}

func timeParam(values url.Values, key string, now time.Time) (time.Time, error) {
	raw := values.Get(key)
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := datetime.Parse(raw, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", key, err)
	}
	return parsed.Time, nil
}
