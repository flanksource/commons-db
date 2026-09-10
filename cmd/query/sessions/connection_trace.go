package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
)

// startConnectionTrace creates an ephemeral profile in the shared session
// registry. Only POST starts capture; connection discovery remains read-only.
func (h *sessionHandler) startConnectionTrace(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Database    string   `json:"database"`
		Users       []string `json:"users"`
		Apps        []string `json:"apps"`
		Hosts       []string `json:"hosts"`
		Events      []string `json:"events"`
		MinDuration string   `json:"minDuration"`
		Duration    string   `json:"duration"`
	}
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
	reference := id
	if _, err := uuid.Parse(id); err != nil {
		reference = "connection://" + id
	}
	conn, err := dbcontext.HydrateConnectionByURL(h.ctx.Wrap(r.Context()), reference)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if conn.Type != models.ConnectionTypeSQLServer {
		http.Error(w, "XEvents tracing requires a SQL Server connection", http.StatusBadRequest)
		return
	}
	profile := query.Profile{
		Name: "connection-" + conn.ID.String() + "-sql-xevent", Virtual: true,
		Provider: query.ProviderConfig{Type: (sqlXEventProvider{}).Type(), Connection: reference, Options: map[string]any{
			"database": input.Database, "users": input.Users, "apps": input.Apps,
			"hosts": input.Hosts, "events": input.Events, "minDuration": input.MinDuration,
		}},
		Trace: &query.TraceSpec{MaxDuration: types.Duration{Duration: duration}},
	}
	session, err := query.ExecuteStream(h.ctx, h.registry, profile)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, query.ErrMaxSessions) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeSessionJSON(w, http.StatusCreated, session.Snapshot())
}
