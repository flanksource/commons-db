// Package query is the query trace kind: a commons-db query profile, saved or
// read in place through a provider's typed options, read once over a window,
// once when the capture stops, or every interval, each row a record.
package query

import (
	"context"
	"errors"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	dbquery "github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// ProfileResolver resolves a saved profile by name, imports applied, as the
// host's profile store holds it.
type ProfileResolver interface {
	Resolve(ctx context.Context, name string) (dbquery.Profile, error)
}

// Options are the host seams the kind reads through.
type Options struct {
	// Sampled requires an emitter that atomically publishes complete read windows.
	Sampled bool
	// Profiles resolves a params' saved profile; nil refuses every capture
	// that names one.
	Profiles ProfileResolver
	// BeforeExecute prepares, and may refuse, every read before it runs, as
	// every other read of a profile's data is prepared.
	BeforeExecute profilestore.BeforeExecuteFunc
}

// Row is one row a read returned.
type Row struct {
	// Time is the row's time column, time or timestamp; a row of a read that
	// has none is timed by the moment it was read.
	Time time.Time `json:"time" pretty:"label=Time" sort:"time"`
	// Profile is the saved profile read; empty for a provider read in place.
	Profile  string `json:"profile,omitempty" filter:"terms"`
	Provider string `json:"provider" filter:"terms"`
	// Row is the provider's row as it returned it.
	Values map[string]any `json:"row" pretty:"hide"`
}

// Reads is the kind's trace handler: a host whose tracing runtime is not
// commons-db's wraps it in its own handler, adapting the emitter it passes.
type Reads struct{ options Options }

// NewReads is the kind's handler over options' seams.
func NewReads(options Options) (Reads, error) {
	if options.BeforeExecute == nil {
		return Reads{}, errors.New("the query kind requires a BeforeExecute to prepare each read")
	}
	return Reads{options: options}, nil
}

func (Reads) Params() Params { return Params{} }

func (Reads) Schema() recordresults.ResultType[Row] {
	return recordresults.ResultType[Row]{Title: "Query profile reads", TimeColumn: "time"}
}

type readerKey struct{}

// Prepare resolves the profile and checks it can be windowed as the params
// ask, so a capture that could not read refuses its start.
func (r Reads) Prepare(ctx dbcontext.Context, params Params, _ tracing.Emitter[Row]) (dbcontext.Context, func() error, error) {
	read, err := r.reader(ctx, params, time.Now().UTC())
	if err != nil {
		return ctx, nil, err
	}
	return ctx.WithValue(readerKey{}, read), nil, nil
}

// Handle reads once and ends; with UntilStopped it reads when the capture
// stops, and with Every it reads at each interval until it stops.
func (Reads) Handle(ctx dbcontext.Context, params Params, records tracing.Emitter[Row], _ tracing.Records[Row]) error {
	read, ok := ctx.Value(readerKey{}).(*reader)
	if !ok {
		return errors.New("query capture handled without the reader its Prepare set up")
	}
	every, err := params.every()
	if err != nil {
		return err
	}
	switch {
	case every > 0:
		return read.follow(ctx, every, records)
	case params.UntilStopped:
		<-ctx.Done()
		// A stopped capture still takes what its handler emits before it
		// returns, so the read outlives the stop that triggered it.
		stopped := ctx.Wrap(context.WithoutCancel(ctx))
		return read.once(stopped, time.Now().UTC(), records)
	default:
		return read.once(ctx, time.Now().UTC(), records)
	}
}

// Capabilities are what a query capture can do: read a past window, and follow
// with Every.
var Capabilities = tracing.Capabilities{Historical: true, Follow: true}

// Kind is the query trace kind over options' seams.
func Kind(options Options) (*tracing.Handler[Params, Row], error) {
	handler, err := NewReads(options)
	if err != nil {
		return nil, err
	}
	return tracing.NewHandler[Params, Row](handler, Capabilities), nil
}
