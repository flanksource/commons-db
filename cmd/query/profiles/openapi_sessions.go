package profiles

import (
	"slices"

	"github.com/flanksource/clicky/rpc"

	"github.com/flanksource/commons-db/query"
)

// profileSessionStart is the session a profile can be started as, if any: the
// trace or top session it declares, or — for a query profile whose provider
// streams — a follow of it, which the start must ask for with follow=true.
// A query profile whose provider answers once has no session to offer.
func profileSessionStart(profile query.Profile, entityName string, parameters []rpc.OpenAPIParameter) (rpc.OpenAPIOperation, bool) {
	const lifecycle = "follow it via GET /api/v1/sessions/{id}/events (SSE) and stop it via DELETE /api/v1/sessions/{id}. " +
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
