package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/context"
)

// StreamProvider is an optional provider capability for continuous sources
// (log tails, event streams). Trace profiles require it.
type StreamProvider interface {
	Provider

	// Stream runs req, calling emit for each row until ctx is cancelled or the
	// source ends. It blocks; a nil return means the source ended normally.
	Stream(ctx context.Context, req ProviderRequest, emit func(Row)) error
}

// ExecuteStream starts a trace or top session and returns immediately with the
// session in the starting state. ctx must be a long-lived application context;
// the run is bounded only by the session's clamped MaxDuration or Stop().
func ExecuteStream(ctx context.Context, reg *SessionRegistry, p Profile, params ...map[string]any) (*Session, error) {
	if err := p.ValidateKind(); err != nil {
		return nil, err
	}
	if p.Kind() == KindQuery {
		return nil, fmt.Errorf("profile %q declares neither trace nor top; use Execute", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	var supplied map[string]any
	if len(params) > 0 {
		supplied = params[0]
	}
	resolved, err := resolveParams(p.Params, supplied)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	if p.Kind() == KindTrace {
		return startTrace(ctx, reg, p, resolved)
	}
	return startTop(ctx, reg, p, resolved)
}

func startTrace(ctx context.Context, reg *SessionRegistry, p Profile, resolved map[string]any) (*Session, error) {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	sp, ok := provider.(StreamProvider)
	if !ok {
		return nil, fmt.Errorf("profile %q: provider %q does not support streaming", p.Name, p.Provider.Type)
	}
	rendered, err := renderQuery(ctx, p.Query, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	session := newRegisteredSession(reg, p, resolved, reg.ClampEvents(p.Trace.EventLimit()))
	if err := reg.Add(session); err != nil {
		return nil, err
	}
	runCtx, cancel := ctx.WithTimeout(reg.ClampDuration(p.Trace.DurationLimit()))
	session.setCancel(cancel)

	go runTrace(runCtx, cancel, sp, session, p, ProviderRequest{
		Connection: p.Provider.Connection,
		Query:      rendered,
		Options:    p.Provider.Options,
	})
	return session, nil
}

func runTrace(ctx context.Context, cancel stdcontext.CancelFunc, sp StreamProvider, s *Session, p Profile, req ProviderRequest) {
	defer cancel()
	s.markRunning()

	var emitErr error
	var once sync.Once
	err := sp.Stream(ctx, req, func(row Row) {
		rows := []Row{row}
		if cerr := applyColumns(ctx, p.Columns, rows); cerr != nil {
			once.Do(func() {
				emitErr = fmt.Errorf("profile %q: %w", p.Name, cerr)
				cancel()
			})
			return
		}
		s.Emit(Event{Row: rows[0]})
	})
	if emitErr != nil {
		err = emitErr
	}
	s.markDone(normalizeStreamErr(err))
}

func startTop(ctx context.Context, reg *SessionRegistry, p Profile, resolved map[string]any) (*Session, error) {
	if _, err := GetProvider(p.Provider.Type); err != nil {
		return nil, err
	}
	session := newRegisteredSession(reg, p, resolved, reg.ClampEvents(0))
	if err := reg.Add(session); err != nil {
		return nil, err
	}
	runCtx, cancel := ctx.WithTimeout(reg.ClampDuration(p.Top.DurationLimit()))
	session.setCancel(cancel)

	go runTop(runCtx, cancel, session, p, resolved)
	return session, nil
}

func runTop(ctx context.Context, cancel stdcontext.CancelFunc, s *Session, p Profile, resolved map[string]any) {
	defer cancel()
	s.markRunning()

	ticker := time.NewTicker(p.Top.TickInterval())
	defer ticker.Stop()
	for {
		result, err := executeResolved(ctx, p, resolved)
		if err != nil {
			if norm := normalizeStreamErr(err); norm != nil {
				s.Emit(Event{Error: norm.Error()})
				s.markDone(norm)
			} else {
				s.markDone(nil)
			}
			return
		}
		s.setLatest(result)
		s.Emit(Event{Rows: result.Rows})

		select {
		case <-ctx.Done():
			s.markDone(nil)
			return
		case <-ticker.C:
		}
	}
}

func newRegisteredSession(reg *SessionRegistry, p Profile, resolved map[string]any, maxEvents int) *Session {
	return NewSession(SessionOptions{
		ID:           uuid.NewString(),
		Profile:      p,
		Params:       resolved,
		MaxEvents:    maxEvents,
		OnEvent:      reg.opts.OnEvent,
		OnTransition: reg.opts.OnTransition,
	})
}

// normalizeStreamErr treats cancellation and the session's own deadline as a
// normal end of stream, not a failure.
func normalizeStreamErr(err error) error {
	if err == nil || errors.Is(err, stdcontext.Canceled) || errors.Is(err, stdcontext.DeadlineExceeded) {
		return nil
	}
	return err
}
