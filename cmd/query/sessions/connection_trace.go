package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
)

// connectionTraceProfile is the virtual profile a connection's trace sessions
// are authorized and recorded under.
func connectionTraceProfile(id uuid.UUID) string {
	return "connection-" + id.String() + "-sql-xevent"
}

// connectionTraceInput is the body a connection trace is started with.
type connectionTraceInput struct {
	Database    string   `json:"database"`
	Users       []string `json:"users"`
	Apps        []string `json:"apps"`
	Hosts       []string `json:"hosts"`
	Events      []string `json:"events"`
	MinDuration string   `json:"minDuration"`
	Duration    string   `json:"duration"`
}

// startConnectionTrace creates an ephemeral profile in the shared session
// registry. Only POST starts capture; connection discovery remains read-only.
func (h *sessionHandler) startConnectionTrace(w http.ResponseWriter, r *http.Request, id string) {
	var input connectionTraceInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	duration := 5 * time.Minute
	if input.Duration != "" {
		var err error
		duration, err = time.ParseDuration(input.Duration)
		if err != nil || duration <= 0 {
			http.Error(w, "duration must be positive (for example 5m)", http.StatusBadRequest)
			return
		}
	}
	reference, conn, ok := h.traceableConnection(w, r, id)
	if !ok {
		return
	}
	if conn.Type != models.ConnectionTypeSQLServer {
		http.Error(w, "XEvents tracing requires a SQL Server connection", http.StatusBadRequest)
		return
	}
	profile := query.Profile{
		Name: connectionTraceProfile(conn.ID), Virtual: true,
		Provider: query.ProviderConfig{Type: providers.SQLXEventProviderType, Connection: reference, Options: map[string]any{
			// The server-side session carries the same name this connection's
			// trace profile does, so a DBA reading sys.dm_xe_sessions can tell
			// which connection asked for it.
			"sessionName": connectionTraceProfile(conn.ID),
			"database":    input.Database, "users": input.Users, "apps": input.Apps,
			"hosts": input.Hosts, "events": input.Events, "minDuration": input.MinDuration,
		}},
		Trace: &query.TraceSpec{MaxDuration: types.Duration{Duration: duration}},
	}
	session, err := query.ExecuteStream(h.sessionContext(r), h.registry, profile)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, query.ErrMaxSessions) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	h.writeLive(w, r, http.StatusCreated, session)
}

// traceableConnection resolves the connection id names, with the reference it
// was looked up by, once r may start a trace of it. A connection a caller may
// not trace is answered exactly as one that does not exist: a uuid names the
// virtual profile before anything is looked up, so it is authorized first; a
// name is authorized once it resolves to one.
func (h *sessionHandler) traceableConnection(w http.ResponseWriter, r *http.Request, id string) (string, *models.Connection, bool) {
	reference, notFound := id, fmt.Sprintf("connection %q not found", id)
	if parsed, err := uuid.Parse(id); err != nil {
		reference = "connection://" + id
		notFound = fmt.Sprintf("connection %q not found", reference)
	} else if h.refusal(r, connectionTraceProfile(parsed), ActionControl) != nil {
		http.Error(w, notFound, http.StatusNotFound)
		return "", nil, false
	}
	conn, err := dbcontext.HydrateConnectionByURL(h.ctx.Wrap(r.Context()), reference)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return "", nil, false
	}
	if h.refusal(r, connectionTraceProfile(conn.ID), ActionControl) != nil {
		http.Error(w, notFound, http.StatusNotFound)
		return "", nil, false
	}
	return reference, conn, true
}
