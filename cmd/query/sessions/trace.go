package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query"
)

type RunnerOptions struct {
	Profiles ProfileStoreProvider
	Context  ContextProvider
	Stdout   io.Writer
	Stderr   io.Writer
}

type Runner struct {
	profiles ProfileStoreProvider
	context  ContextProvider
	stdout   io.Writer
	stderr   io.Writer
}

func NewRunner(options RunnerOptions) (*Runner, error) {
	if options.Profiles == nil {
		return nil, fmt.Errorf("profile store provider is required")
	}
	if options.Context == nil {
		return nil, fmt.Errorf("context provider is required")
	}
	if options.Stdout == nil || options.Stderr == nil {
		return nil, fmt.Errorf("session output writers are required")
	}
	return &Runner{profiles: options.Profiles, context: options.Context, stdout: options.Stdout, stderr: options.Stderr}, nil
}

type TraceOptions struct {
	Duration string
	Params   []string
	Output   string
}

func (r *Runner) RunTrace(ctx context.Context, name string, options TraceOptions) error {
	store, err := r.profiles()
	if err != nil {
		return err
	}
	resolved, err := profiles.Resolve(ctx, store, name)
	if err != nil {
		return err
	}
	p := resolved.Profile
	if p.Kind() != query.KindTrace {
		return fmt.Errorf("profile %q is not a trace; use `query top` to sample it", p.Name)
	}
	if p, err = applySessionSpecOverrides(p, "", options.Duration); err != nil {
		return err
	}
	params, err := parseSessionParams(options.Params)
	if err != nil {
		return err
	}

	session, err := r.startCLISession(p, params)
	if err != nil {
		return err
	}
	replay, live, cancelSub := session.Subscribe()
	defer cancelSub()

	sigCtx, cancelSig := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancelSig()
	go func() {
		<-sigCtx.Done()
		session.Stop()
	}()

	for _, e := range replay {
		r.printTraceEvent(e, options.Output)
	}
	for e := range live {
		r.printTraceEvent(e, options.Output)
	}
	return r.finishCLISession(session)
}

// startCLISession runs the profile through a single-slot in-memory registry;
// CLI sessions are not persisted.
func (r *Runner) startCLISession(p query.Profile, params map[string]any) (*query.Session, error) {
	registry := query.NewSessionRegistry(query.RegistryOptions{
		MaxSessions: 1,
		MaxDuration: sessionSpecDuration(p),
	})
	return query.ExecuteStream(r.context(), registry, p, params)
}

func sessionSpecDuration(p query.Profile) time.Duration {
	if p.Trace != nil {
		return p.Trace.DurationLimit()
	}
	return p.Top.DurationLimit()
}

// finishCLISession prints the session summary and surfaces a failure as a
// non-zero exit.
func (r *Runner) finishCLISession(session *query.Session) error {
	snap := session.Snapshot()
	elapsed := time.Since(snap.StartedAt).Round(time.Millisecond)
	if snap.StoppedAt != nil {
		elapsed = snap.StoppedAt.Sub(snap.StartedAt).Round(time.Millisecond)
	}
	fmt.Fprintf(r.stderr, "session %s: %s, %d events in %s\n", snap.ID, snap.State, snap.EventCount, elapsed)
	if snap.State == query.SessionFailed {
		return errors.New(snap.Error)
	}
	return nil
}

func parseSessionParams(pairs []string) (map[string]any, error) {
	params := map[string]any{}
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --param %q: expected key=value", pair)
		}
		params[k] = v
	}
	return params, nil
}

func (r *Runner) printTraceEvent(e query.Event, format string) {
	if format == "json" {
		data, err := json.Marshal(e)
		if err != nil {
			fmt.Fprintf(r.stderr, "encode event %d: %v\n", e.Sequence, err)
			return
		}
		fmt.Fprintln(r.stdout, string(data))
		return
	}
	fmt.Fprintln(r.stdout, formatTraceEventLine(e))
}

// formatTraceEventLine renders an event as one logfmt line: timestamp then
// sorted key=value pairs, quoting values with spaces.
func formatTraceEventLine(e query.Event) string {
	var b strings.Builder
	b.WriteString(e.Time.UTC().Format(time.RFC3339))
	if e.Error != "" {
		fmt.Fprintf(&b, " error=%s", logfmtValue(e.Error))
		return b.String()
	}
	keys := make([]string, 0, len(e.Row))
	for k := range e.Row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%s", k, logfmtValue(fmt.Sprint(e.Row[k])))
	}
	return b.String()
}

func logfmtValue(v string) string {
	if strings.ContainsAny(v, " \t\"=") {
		return strconv.Quote(v)
	}
	return v
}
