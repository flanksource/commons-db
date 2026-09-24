package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing/xetrace"
)

func init() { query.RegisterProvider(sqlXEventProvider{}) }

// SQLXEventProviderType names the Extended Events provider in a profile's
// `provider.type`. A profile naming it must also declare `trace:` — the
// capture has no single-shot form, so Execute refuses.
const SQLXEventProviderType = "sqlserver-xevent"

// sqlXEventProvider captures live SQL Server statements through an Extended
// Events session on a ring_buffer target: it creates the session, drains it
// for as long as the caller streams, and drops it on the way out.
type sqlXEventProvider struct{}

func (sqlXEventProvider) Type() string { return SQLXEventProviderType }

// Execute refuses rather than returning an empty page. A trace reads events
// that do not exist until a session is running, so a caller that reached here
// asked the wrong question — usually a profile that named this provider and
// forgot its `trace:` block.
func (sqlXEventProvider) Execute(context.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, fmt.Errorf(
		"provider %q captures events live and has no single-shot form; declare `trace:` on the profile and start a session",
		SQLXEventProviderType)
}

// sqlXEventOptions are the capture knobs a profile sets under
// `provider.options`. Users/Apps/Hosts are pushed into the Extended Events
// predicate so they filter inside SQL Server; Types/Tables are applied to the
// parsed events, because the predicate has no structured statement type or
// referenced-object to match on. All three take collections.MatchItem patterns
// (exact, `*` wildcard, `!` exclusion).
//
// The sizing and poll knobs are zero by default so an unset one means "take
// the documented default" rather than a number this struct has to keep in step
// with xetrace.
type sqlXEventOptions struct {
	// SessionName names the Extended Events session on the server. Required:
	// the session is visible in sys.dm_xe_sessions to everyone on the instance,
	// so what it is called is the profile author's to say, not this package's.
	// A unique suffix is appended so concurrent captures of one profile do not
	// collide on the server.
	SessionName string `json:"sessionName"`

	// Database scopes the session to one database; empty uses the connection's.
	Database string `json:"database,omitempty"`
	// AllDatabases captures across the whole instance instead of one database.
	// It is separate from an empty Database because that already means "the
	// connection's own", and instance-wide is the wider scope of the two.
	AllDatabases bool `json:"allDatabases,omitempty"`

	Users []string `json:"users,omitempty"`
	Apps  []string `json:"apps,omitempty"`
	Hosts []string `json:"hosts,omitempty"`

	// Events selects the Extended Events to capture; empty uses xetrace's
	// defaults. Add sp_statement_completed to see statements inside procedures.
	Events []string `json:"events,omitempty"`

	// Types narrows to statement types (SELECT/INSERT/UPDATE/DELETE/MERGE/EXEC/DML).
	Types []string `json:"types,omitempty"`
	// Tables narrows to referenced tables, or a procedure name for an EXEC.
	Tables []string `json:"tables,omitempty"`

	// MinDuration drops statements faster than this threshold, e.g. "10ms".
	MinDuration string `json:"minDuration,omitempty"`
	// Poll is the ring-buffer poll cadence, e.g. "1s". Empty uses xetrace's.
	Poll string `json:"poll,omitempty"`

	// MaxEvents caps the ring buffer's event count; MaxMemoryKB its size.
	MaxEvents   int `json:"maxEvents,omitempty"`
	MaxMemoryKB int `json:"maxMemoryKb,omitempty"`
}

// decodeXEventOptions reads the profile's options, refusing any key this
// provider does not define.
//
// The shared decoder ignores what it does not recognise, which for a capture is
// the wrong answer: a misspelled `minDuration` would be dropped and the session
// would run unfiltered, recording every statement on the instance instead of the
// slow ones the author asked for. Nothing about that looks wrong from the
// outside, so it has to be refused at the point the profile is read.
func decodeXEventOptions(options map[string]any) (sqlXEventOptions, error) {
	var decoded sqlXEventOptions
	if len(options) == 0 {
		return decoded, nil
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return decoded, fmt.Errorf("encode provider options: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return decoded, fmt.Errorf("provider %q options: %w", SQLXEventProviderType, err)
	}
	return decoded, nil
}

// captureOptions projects the decoded options onto the two xetrace option sets,
// rejecting any value the session could not honour.
//
// It is separate from Stream so a bad profile fails at decode with the option
// named, rather than as an opaque SQL Server syntax error once a session is
// half-created.
func (o sqlXEventOptions) captureOptions() (xetrace.CreateOptions, xetrace.DrainOptions, error) {
	var create xetrace.CreateOptions
	var drain xetrace.DrainOptions

	if strings.TrimSpace(o.SessionName) == "" {
		return create, drain, fmt.Errorf("sessionName is required: it names the Extended Events session on the server, where it is visible to everyone on the instance")
	}
	events, err := xetrace.NormalizeEvents(o.Events)
	if err != nil {
		return create, drain, err
	}
	minimum, err := parsePositiveDuration("minDuration", o.MinDuration)
	if err != nil {
		return create, drain, err
	}
	poll, err := parsePositiveDuration("poll", o.Poll)
	if err != nil {
		return create, drain, err
	}
	if o.MaxEvents < 0 {
		return create, drain, fmt.Errorf("maxEvents %d: must not be negative", o.MaxEvents)
	}
	if o.MaxMemoryKB < 0 {
		return create, drain, fmt.Errorf("maxMemoryKb %d: must not be negative", o.MaxMemoryKB)
	}

	if o.AllDatabases && o.Database != "" {
		return create, drain, fmt.Errorf("database %q and allDatabases are mutually exclusive: capture one database or the whole instance, not both", o.Database)
	}

	create = xetrace.CreateOptions{
		Name:              uniqueSessionName(o.SessionName),
		DatabaseName:      o.Database,
		AllDatabases:      o.AllDatabases,
		Users:             o.Users,
		Apps:              o.Apps,
		Hosts:             o.Hosts,
		Events:            events,
		MinDurationMicros: minimum.Microseconds(),
		MaxEvents:         o.MaxEvents,
		MaxMemoryKB:       o.MaxMemoryKB,
		Filter:            xetrace.EventFilter{Types: o.Types, Tables: o.Tables},
	}
	drain = xetrace.DrainOptions{Interval: poll}
	return create, drain, nil
}

// uniqueSessionName appends a short unique suffix to the caller's name, so two
// captures started from one profile do not collide on a server-scoped object
// while the name still says whose they are.
func uniqueSessionName(base string) string {
	return strings.TrimSpace(base) + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

// parsePositiveDuration reads an optional duration option. Empty returns zero,
// which every xetrace knob reads as "use the default"; a negative one would
// either busy-loop the drain or render into the session predicate, so it is
// rejected rather than clamped.
func parsePositiveDuration(name, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, value, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s %q: must not be negative", name, value)
	}
	return parsed, nil
}

func (sqlXEventProvider) Stream(ctx context.Context, req query.ProviderRequest, emit func(query.Row)) (err error) {
	options, err := decodeXEventOptions(req.Options)
	if err != nil {
		return err
	}
	create, drain, err := options.captureOptions()
	if err != nil {
		return fmt.Errorf("provider %q: %w", SQLXEventProviderType, err)
	}

	conn := connection.SQLConnection{ConnectionName: req.Connection}
	if err := conn.HydrateConnection(ctx); err != nil {
		return err
	}
	if conn.Type != models.ConnectionTypeSQLServer {
		return fmt.Errorf("SQL XEvents requires a SQL Server connection")
	}
	release, err := ctx.AcquireConnectionLease(req.Connection)
	if err != nil {
		return err
	}
	defer release()
	if create.DatabaseName != "" {
		conn, err = conn.UseDatabase(create.DatabaseName)
		if err != nil {
			return err
		}
	}
	client, err := conn.Client(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := xetrace.Create(ctx, client, create)
	if err != nil {
		return err
	}
	defer func() {
		if failure := errors.Join(err, session.Drop(ctx)); failure != nil {
			// Final polling and teardown use independent deadlines. Their failure
			// must not be normalized as the caller's ordinary cancellation.
			err = fmt.Errorf("SQL XEvents capture failed: %v", failure)
		}
	}()

	drain.OnEvent = func(event xetrace.Event) { emit(eventRow(event)) }
	drain.OnDropped = func(delta int64, stats xetrace.RingBufferStats) {
		emit(query.Row{
			"timestamp": time.Now().UTC(),
			"name":      "warning",
			"error_message": fmt.Sprintf(
				"SQL Server ring buffer lost or truncated events (unseen: %d, dropped: %d, truncated: %t)",
				delta, stats.DroppedCount, stats.Truncated),
		})
	}
	return xetrace.Drain(ctx, session, drain)
}

// eventRow converts a captured event to a generic row through its own JSON
// encoding, so the row keys are the Event field tags — the contract the trace
// tab, the profile columns and any stored session result all read.
func eventRow(event xetrace.Event) query.Row {
	data, _ := json.Marshal(event)
	var row query.Row
	_ = json.Unmarshal(data, &row)
	return row
}
