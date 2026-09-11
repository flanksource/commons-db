package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing/xetrace"
)

func init() { query.RegisterProvider(sqlXEventProvider{}) }

// sqlXEventProvider adapts the connection playground to the session registry.
// It is private to the query app, not part of the reusable provider catalog.
type sqlXEventProvider struct{}

func (sqlXEventProvider) Type() string { return "connection-sql-xevent" }

func (sqlXEventProvider) Execute(context.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, fmt.Errorf("start SQL XEvents from the SQL Server connection's Trace tab")
}

type sqlXEventOptions struct {
	Database    string   `json:"database,omitempty"`
	Users       []string `json:"users,omitempty"`
	Apps        []string `json:"apps,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`
	Events      []string `json:"events,omitempty"`
	MinDuration string   `json:"minDuration,omitempty"`
}

func (sqlXEventProvider) Stream(ctx context.Context, req query.ProviderRequest, emit func(query.Row)) (err error) {
	opts, err := query.DecodeOptions[sqlXEventOptions](req.Options)
	if err != nil {
		return err
	}
	events, err := xetrace.NormalizeEvents(opts.Events)
	if err != nil {
		return err
	}
	var minimum time.Duration
	if opts.MinDuration != "" {
		minimum, err = time.ParseDuration(opts.MinDuration)
		if err != nil || minimum < 0 {
			return fmt.Errorf("invalid minimum duration %q: expected a non-negative duration", opts.MinDuration)
		}
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
	if opts.Database != "" {
		conn, err = conn.UseDatabase(opts.Database)
		if err != nil {
			return err
		}
	}
	client, err := conn.Client(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := xetrace.Create(ctx, client, xetrace.CreateOptions{
		DatabaseName: opts.Database,
		Users:        opts.Users, Apps: opts.Apps, Hosts: opts.Hosts,
		Events: events, MinDurationMicros: minimum.Microseconds(),
	})
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
	return xetrace.Drain(ctx, session, xetrace.DrainOptions{
		OnEvent: func(event xetrace.Event) {
			// Preserve the migrated event's JSON contract, including RPC metadata.
			data, _ := json.Marshal(event)
			var row query.Row
			_ = json.Unmarshal(data, &row)
			emit(row)
		},
		OnDropped: func(delta int64, stats xetrace.RingBufferStats) {
			emit(query.Row{"timestamp": time.Now().UTC(), "name": "warning", "error_message": fmt.Sprintf("SQL Server ring buffer lost or truncated events (unseen: %d, dropped: %d, truncated: %t)", delta, stats.DroppedCount, stats.Truncated)})
		},
	})
}
