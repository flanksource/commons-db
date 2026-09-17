package profiles

import (
	"slices"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"

	"github.com/flanksource/commons-db/query"
)

// profileSessionStart is the session a profile can be started as, if any: the
// trace or top session it declares, or — for a query profile whose provider
// streams — a follow of it, which the start must ask for with follow=true.
// A query profile whose provider answers once has no session to offer.
func profileSessionStart(profile query.Profile, entityName string, parameters []rpc.OpenAPIParameter) (rpc.OpenAPIOperation, bool) {
	const lifecycle = "follow it via GET /api/v1/sessions/{id}/events (SSE) and stop it via POST /api/v1/sessions/{id}/stop. " +
		"Each event frame's data is a session event: a trace event's row is the raw row, and its clickyRow is the same row " +
		"exactly as this profile's application/json+clicky table page presents it (node.rows[n])"
	start := rpc.OpenAPIOperation{
		Summary:     "Start a " + string(profile.Kind()) + " session for " + profile.Name,
		Description: "Start a live session; " + lifecycle,
		OperationID: "start-" + entityName + "-session",
		Parameters:  parameters,
		Responses:   map[string]rpc.OpenAPIResponse{"201": {Description: "Session started"}},
	}
	switch {
	case profile.Kind() != query.KindQuery:
		return start, true
	case !query.SupportsStreaming(profile.Provider.Type):
		return rpc.OpenAPIOperation{}, false
	}
	start.Summary = "Follow " + profile.Name
	start.Description = "Start a session with follow=true that tails the profile's rows from here onward; " + lifecycle
	// Cloned so the follow flag never lands in the list operation's parameters,
	// which share the backing array.
	start.Parameters = append(slices.Clone(parameters), rpc.OpenAPIParameter{
		Name: "follow", In: "query", Required: true,
		Description: "Tail the profile's source as rows arrive rather than reading it once",
		Schema:      &rpc.OpenAPISchema{Type: "boolean", Enum: []any{true}},
	})
	return start, true
}

const (
	sessionsSurface = "sessions"
	sessionsPath    = "/api/v1/sessions"
	sessionPath     = sessionsPath + "/{id}"
)

// sessionFilter is one filter parameter of the sessions list.
type sessionFilter struct {
	name, label string
	value       bool // a single typed value rather than a multi-select
	enum        []string
}

func sessionFilters() []sessionFilter {
	states := []string{string(query.SessionStarting), string(query.SessionRunning), string(query.SessionStopping),
		string(query.SessionCompleted), string(query.SessionFailed), string(query.SessionStopped), string(query.SessionInterrupted)}
	filters := []sessionFilter{
		{name: "profile", label: "Profile"},
		{name: "kind", label: "Kind", enum: []string{string(query.KindTrace), string(query.KindTop), string(query.KindCapture)}},
		{name: "state", label: "State", enum: states},
		{name: "principal", label: "Principal"},
		{name: "restartOf", label: "Restart of", value: true},
	}
	for _, key := range []string{"target", "origin", "via", "runId", "plan", "step", "environment"} {
		filters = append(filters, sessionFilter{name: "label." + key, label: key})
	}
	return filters
}

// AddSessionsOpenAPI emits the sessions API — the list with its filters,
// window, sort and paging, one session, and its stop, extend and restart
// actions — as the "sessions" surface a clicky-ui OperationCatalog renders.
func AddSessionsOpenAPI(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	if spec.Clicky == nil {
		spec.Clicky = &rpc.ClickySpecMeta{}
	}
	spec.Clicky.Surfaces = append(spec.Clicky.Surfaces, rpc.ClickySurface{Key: sessionsSurface, Entity: sessionsSurface, Title: "Sessions"})
	spec.Paths[sessionsPath] = rpc.OpenAPIPath{"get": sessionListOperation(spec)}
	spec.Paths[sessionPath] = rpc.OpenAPIPath{"get": {
		OperationID: "get-session", Summary: "Get a session", Parameters: []rpc.OpenAPIParameter{sessionIDParameter()},
		Responses: map[string]rpc.OpenAPIResponse{"200": {Description: "SessionInfo"}, "404": {Description: "No such session"}},
		Clicky:    &rpc.ClickyOperationMeta{Command: sessionsSurface, Surface: sessionsSurface, Verb: "get", Scope: "entity", IDParam: "id"},
	}}
	spec.Paths[sessionPath+"/stop"] = rpc.OpenAPIPath{"post": sessionAction("stop-session", "Stop a session", nil)}
	spec.Paths[sessionPath+"/extend"] = rpc.OpenAPIPath{"post": sessionAction("extend-session", "Move a session's deadline later",
		sessionDurationParameter(true, "Go duration added to stopAt, e.g. 900000ms or 15m; clamped to the registry's MaxDuration from the start"))}
	restart := sessionAction("restart-session", "Start a new session from an ended one's params",
		sessionDurationParameter(false, "Go duration for the new run, recorded as its params.durationMs; absent replays the ended "+
			"session's params.durationMs — the duration it was first requested for, not a run an extension lengthened"))
	restart.Responses["201"] = rpc.OpenAPIResponse{Description: "The new session (SessionInfo) with restartOf set"}
	restart.Responses["409"] = rpc.OpenAPIResponse{Description: "Not terminal, or not restartable here"}
	delete(restart.Responses, "200")
	spec.Paths[sessionPath+"/restart"] = rpc.OpenAPIPath{"post": restart}
}

func sessionListOperation(spec *rpc.OpenAPISpec) rpc.OpenAPIOperation {
	var parameters []rpc.OpenAPIParameter
	for _, filter := range sessionFilters() {
		component := "sessions-" + strings.ReplaceAll(filter.name, ".", "-")
		shape := "multi-filter"
		if filter.value {
			shape = "value"
		}
		ensureProfileFilterComponent(spec, entity.FilterSpec{Name: component, Label: filter.label, Type: shape, Multi: !filter.value})
		schema := &rpc.OpenAPISchema{Type: "string"}
		for _, value := range filter.enum {
			schema.Enum = append(schema.Enum, value)
		}
		parameters = append(parameters, rpc.OpenAPIParameter{
			Name: filter.name, In: "query", Schema: schema, Clicky: &rpc.ClickyParameterMeta{Role: "filter"},
			Description: "Include or exclude " + filter.label + " values: comma-separated, a leading ! excludes, * matches " +
				"any run of characters. Repeated keys (?state=running&state=stopping) are the same selection.",
			Lookup: &rpc.ClickyLookupMeta{
				Ref: "#/components/x-clicky-filters/" + component, URL: sessionsPath, Filter: filter.name,
				SearchParam: "__lookup_q", Multi: !filter.value,
			},
		})
	}
	return rpc.OpenAPIOperation{
		OperationID: "list-sessions", Summary: "List sessions",
		Description: "Capture sessions live in this process or persisted in the session store, authorized per profile " +
			"before totals, paging and lookups.",
		Parameters: append(parameters, sessionListPagingParameters()...),
		Responses: map[string]rpc.OpenAPIResponse{"200": {
			Description: "application/json: {items: SessionInfo[], total, shared}; application/json+clicky: a table document. " +
				"Paging travels in X-Total-Count / X-Page-Limit / X-Page-Offset, and X-Sessions-Shared says whether the store is shared.",
		}},
		Clicky: &rpc.ClickyOperationMeta{Command: sessionsSurface, Surface: sessionsSurface, Verb: "list", Scope: "collection"},
	}
}

func sessionListPagingParameters() []rpc.OpenAPIParameter {
	param := func(name, role, description string, schema *rpc.OpenAPISchema) rpc.OpenAPIParameter {
		return rpc.OpenAPIParameter{Name: name, In: "query", Description: description, Schema: schema, Clicky: &rpc.ClickyParameterMeta{Role: role}}
	}
	sortColumns := make([]any, len(query.SessionSortFields))
	for i, field := range query.SessionSortFields {
		sortColumns[i] = field
	}
	return []rpc.OpenAPIParameter{
		param("from", "time-from", "Started from", &rpc.OpenAPISchema{Type: "string", Format: "date-time"}),
		param("to", "time-to", "Started until", &rpc.OpenAPISchema{Type: "string", Format: "date-time"}),
		param("sort", "sort", "Column to order by; id always breaks ties", &rpc.OpenAPISchema{Type: "string", Enum: sortColumns, Default: "startedAt"}),
		param("order", "order", "Direction for sort", &rpc.OpenAPISchema{Type: "string", Enum: []any{"asc", "desc"}, Default: "desc"}),
		param("limit", "limit", "Sessions per page (maximum 500)", &rpc.OpenAPISchema{Type: "integer", Default: 50}),
		param("offset", "offset", "Sessions to skip", &rpc.OpenAPISchema{Type: "integer", Default: 0}),
	}
}

func sessionIDParameter() rpc.OpenAPIParameter {
	return rpc.OpenAPIParameter{Name: "id", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}}
}

func sessionDurationParameter(required bool, description string) []rpc.OpenAPIParameter {
	return []rpc.OpenAPIParameter{{Name: "duration", In: "query", Required: required, Description: description, Schema: &rpc.OpenAPISchema{Type: "string"}}}
}

func sessionAction(operationID, summary string, parameters []rpc.OpenAPIParameter) rpc.OpenAPIOperation {
	return rpc.OpenAPIOperation{
		OperationID: operationID, Summary: summary,
		Parameters: append([]rpc.OpenAPIParameter{sessionIDParameter()}, parameters...),
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {Description: "The session as it stands after the request (SessionInfo)"},
			"403": {Description: "Authorize(r, profile, control) refused"},
			"404": {Description: "No such session"},
			"409": {Description: "The session's state does not allow this action"},
		},
	}
}
