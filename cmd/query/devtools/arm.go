package devtools

// Arming: how a request says "record this one", and how the response says which
// record it became.
//
// The level rides in a header rather than in a server-side mode, so one browser
// tab cannot change what another tab's requests cost, and nothing has to be
// switched off again if that tab is closed.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
)

const (
	// LevelHeader names the capture level, using observability's own level
	// vocabulary rather than a parallel scale.
	LevelHeader = "X-Debug-Level"

	// LevelParam says the same thing for a caller that cannot set a header —
	// a browser following a download link, which builds its own URL.
	LevelParam = "__debug"

	// IDHeader correlates a response with the record it produced.
	IDHeader = "X-Debug-Id"

	// RefreshHeader asks this request to rebuild every metadata lookup it makes
	// instead of reading the cache.
	//
	// A header rather than a console-wide setting, because it is a property of
	// the run you are about to make: leaving it on would quietly turn every
	// subsequent page into a cache miss, which is exactly the cost the cache
	// exists to avoid.
	RefreshHeader = "X-Debug-Refresh-Inspection"
)

// RefreshRequested reports whether a request asked to rebuild what it inspects.
func RefreshRequested(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(RefreshHeader))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// RequestedLevel reads the capture level a request asked for.
//
// An unknown name is an error rather than a default: a client that sent
// "verbose" and silently got "info" would report a bug against the wrong layer.
func RequestedLevel(r *http.Request) (level logger.LogLevel, armed bool, err error) {
	name := strings.TrimSpace(r.Header.Get(LevelHeader))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get(LevelParam))
	}
	if name == "" {
		return 0, false, nil
	}
	parsed, off, ok := observability.ParseLevelName(name)
	if !ok {
		return 0, false, fmt.Errorf("unknown %s %q: expected one of %s",
			LevelHeader, name, strings.Join(observability.SupportedLevelNames(), ", "))
	}
	if off {
		return 0, false, nil
	}
	return parsed, true, nil
}

// ArmOptions configures Arm.
type ArmOptions struct {
	Context dbcontext.Context
	Request *http.Request
	Source  query.ExecutionSource
	// NewID mints the record's correlation handle. Injected so a test can assert
	// on a stable id instead of matching a pattern.
	NewID func() string
}

// Arm returns the context this request's executions must run on, plus the
// recorder collecting them.
//
// A request that asked for nothing gets its context back unchanged and a nil
// recorder, which every recorder method tolerates — so the unarmed path, which
// is nearly every request, costs one header read.
func Arm(options ArmOptions) (dbcontext.Context, *query.Recorder, error) {
	recorder, err := NewRequestRecorder(options)
	if err != nil || recorder == nil {
		return options.Context, nil, err
	}
	return query.WithRecorder(options.Context, recorder), recorder, nil
}

// NewRequestRecorder mints the recorder a request asked for, without needing a
// database context — which the arming middleware does not have, and does not
// need, because the handler that runs the executions lifts the recorder onto its
// own context later.
func NewRequestRecorder(options ArmOptions) (*query.Recorder, error) {
	level, armed, err := RequestedLevel(options.Request)
	if err != nil || !armed {
		return nil, err
	}

	source := options.Source
	if source.Method == "" {
		source.Method = options.Request.Method
	}
	if source.Path == "" {
		source.Path = options.Request.URL.Path
	}
	if source.Query == "" {
		source.Query = redactedQuery(options.Request)
	}

	newID := options.NewID
	if newID == nil {
		newID = newRecordID
	}
	return query.NewRecorder(query.RecorderOptions{
		ID: newID(), Level: level, Source: source,
		RefreshInspection: RefreshRequested(options.Request),
	}), nil
}

// newRecordID mints a correlation handle. Ordering does not ride on it — the
// ring assigns each record a monotonic sequence, which is what a console sorts
// and resumes by.
func newRecordID() string {
	return uuid.NewString()
}

// redactedQuery keeps the request's own query string, minus the marker that
// armed it and minus anything credential-shaped, so a console can offer to
// re-run the request without the caller rebuilding it by hand.
func redactedQuery(r *http.Request) string {
	values := r.URL.Query()
	values.Del(LevelParam)
	if len(values) == 0 {
		return ""
	}
	sanitized := make(map[string]any, len(values))
	for key := range values {
		sanitized[key] = values.Get(key)
	}
	sanitized = query.SanitizeDiagnosticValues(sanitized)
	for key := range values {
		if text, ok := sanitized[key].(string); ok {
			values.Set(key, text)
		}
	}
	return values.Encode()
}

// StampID writes the correlation header and makes sure a browser can read it.
// Handlers downstream add to the expose list with entity.ExposeHeaders rather
// than replacing it, so exposing the id up front is enough.
func StampID(header http.Header, id string) {
	if id == "" {
		return
	}
	header.Set(IDHeader, id)
	entity.ExposeHeaders(header, IDHeader)
}
