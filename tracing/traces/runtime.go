// Starts trace captures as managed sessions: each runs under the host's session
// registry, and a probe moves its records into a stream of the results store.

package traces

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// ProfilePrefix names every trace session's profile, traces/<kind>, which keys
// its authorization and restart.
const ProfilePrefix = "traces/"

var (
	ErrUnknownKind   = errors.New("unknown trace kind")
	ErrInvalidParams = errors.New("invalid trace params")
)

// Runtime starts captures of the kinds it serves.
type Runtime struct {
	Kinds    *Kinds
	Sessions *query.SessionRegistry
	Probes   *probe.Manager
	// Results is the store the captures' records are committed to, opened
	// with Kinds.RegisterResultTypes.
	Results *recordresults.Results

	// PollEvery is how often a running capture's records are committed.
	// Zero is a second.
	PollEvery time.Duration
	// BufferRows caps the records a capture holds before they are committed.
	// Zero is 10,000; it is never more than one sample of the probe commits.
	BufferRows int
	// BufferBytes caps the bytes of records a capture holds before they are
	// committed, measured by their keys and text. Zero is 64MiB.
	BufferBytes int
}

// StartRequest is one capture to start.
type StartRequest struct {
	Kind   string
	Params json.RawMessage
	// StopAt ends the capture; nil runs it for the registry's longest duration.
	StopAt    *time.Time
	Labels    map[string]string
	Principal string
	RestartOf string
}

// Start begins a capture of request.Kind into a new stream, as a managed
// session that ends at StopAt, on a stop, or when a historical capture is
// done. It refuses an unknown kind (ErrUnknownKind) and params the kind
// refuses (ErrInvalidParams) before any session exists.
func (r *Runtime) Start(ctx dbcontext.Context, request StartRequest) (*query.ManagedSession, error) {
	plugin, ok := r.Kinds.Get(request.Kind)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, request.Kind)
	}
	if err := plugin.ValidateParams(request.Params); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}
	var params map[string]any
	if len(request.Params) > 0 {
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidParams, err)
		}
	}
	stream := rand.Text()
	return r.Sessions.Manage(ctx, query.ManageOptions{
		Track: query.TrackOptions{
			Profile: ProfilePrefix + request.Kind, Kind: query.KindCapture, Params: params,
			Labels: request.Labels, Principal: request.Principal, RestartOf: request.RestartOf, StopAt: request.StopAt,
		},
		PollEvery:  r.pollEvery(),
		PullOnPoll: true,
	}, func(armCtx context.Context) (query.ManagedRun, error) {
		var src *source
		run, err := r.Probes.Arm(armCtx, probe.Options{
			Identity: ProfilePrefix + request.Kind + "/" + stream, Stream: stream, Kind: request.Kind,
			Backend: r.Results.Backend, Describe: r.describe,
		}, func(openCtx context.Context) (probe.Source, error) {
			opened, err := plugin.open(ctx.Wrap(openCtx), openOptions{
				kind: request.Kind, stream: stream, params: request.Params, store: r.Results.Backend, bufferRows: r.bufferRows(), bufferBytes: r.bufferBytes(),
			})
			src = opened
			return opened, err
		})
		if err != nil {
			return nil, err
		}
		src.start()
		return captureRun{Run: run, source: src}, nil
	})
}

// Restarter re-arms an ended trace session as a new capture of its kind with
// its params, under a context base gives the restart's request.
func (r *Runtime) Restarter(base func() dbcontext.Context) query.RestartFunc {
	return func(ctx context.Context, _ query.SessionRecord, opts query.TrackOptions) (*query.Session, error) {
		params, err := json.Marshal(opts.Params)
		if err != nil {
			return nil, err
		}
		managed, err := r.Start(base().Wrap(ctx), StartRequest{
			Kind: strings.TrimPrefix(opts.Profile, ProfilePrefix), Params: params, StopAt: opts.StopAt,
			Labels: opts.Labels, Principal: opts.Principal, RestartOf: opts.RestartOf,
		})
		if err != nil {
			return nil, err
		}
		return managed.Session(), nil
	}
}

func (r *Runtime) describe(ctx context.Context, stream string) (*query.EventsRef, error) {
	ref, err := r.Results.Ref(ctx, stream, 0, 0)
	if err != nil {
		return nil, err
	}
	return ref.EventsRef(), nil
}

func (r *Runtime) pollEvery() time.Duration {
	if r.PollEvery > 0 {
		return r.PollEvery
	}
	return time.Second
}

func (r *Runtime) bufferRows() int {
	if r.BufferRows > 0 {
		return r.BufferRows
	}
	return 10_000
}

func (r *Runtime) bufferBytes() int {
	if r.BufferBytes > 0 {
		return r.BufferBytes
	}
	return defaultBufferBytes
}

// captureRun is a probe run that ends with its handler's error too: the probe
// still drains and seals what the handler emitted, and the session then fails.
type captureRun struct {
	*probe.Run
	source *source
}

func (r captureRun) Stop(ctx context.Context) (query.ManagedFinish, error) {
	finish, err := r.Run.Stop(ctx)
	return finish, errors.Join(err, r.source.handlerErr())
}
