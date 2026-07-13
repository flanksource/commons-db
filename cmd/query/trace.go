package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

type traceOptions struct {
	duration string
	params   []string
	output   string
}

func newTraceCmd() *cobra.Command {
	var o traceOptions
	cmd := &cobra.Command{
		Use:   "trace <profile>",
		Short: "Run a trace profile: stream its events until it ends or Ctrl-C stops it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTraceCmd(cmd, args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.duration, "duration", "", "Stop the trace after this long (default: the profile's maxDuration, capped at 15m)")
	f.StringArrayVar(&o.params, "param", nil, "Profile filter param as key=value (repeatable)")
	f.StringVarP(&o.output, "output", "o", "logfmt", "Event output format: logfmt or json")
	return cmd
}

func runTraceCmd(cmd *cobra.Command, name string, o traceOptions) error {
	p, err := currentStore().Get(name)
	if err != nil {
		return err
	}
	if p.Kind() != query.KindTrace {
		return fmt.Errorf("profile %q is not a trace; use `query top` to sample it", p.Name)
	}
	if p, err = applySessionSpecOverrides(p, "", o.duration); err != nil {
		return err
	}
	params, err := parseSessionParams(o.params)
	if err != nil {
		return err
	}

	session, err := startCLISession(p, params)
	if err != nil {
		return err
	}
	replay, live, cancelSub := session.Subscribe()
	defer cancelSub()

	sigCtx, cancelSig := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelSig()
	go func() {
		<-sigCtx.Done()
		session.Stop()
	}()

	for _, e := range replay {
		printTraceEvent(e, o.output)
	}
	for e := range live {
		printTraceEvent(e, o.output)
	}
	return finishCLISession(session)
}

// startCLISession runs the profile through a single-slot in-memory registry;
// CLI sessions are not persisted.
func startCLISession(p query.Profile, params map[string]any) (*query.Session, error) {
	registry := query.NewSessionRegistry(query.RegistryOptions{
		MaxSessions: 1,
		MaxDuration: sessionSpecDuration(p),
	})
	return query.ExecuteStream(currentContext(), registry, p, params)
}

func sessionSpecDuration(p query.Profile) time.Duration {
	if p.Trace != nil {
		return p.Trace.DurationLimit()
	}
	return p.Top.DurationLimit()
}

// finishCLISession prints the session summary and surfaces a failure as a
// non-zero exit.
func finishCLISession(session *query.Session) error {
	snap := session.Snapshot()
	elapsed := time.Since(snap.StartedAt).Round(time.Millisecond)
	if snap.StoppedAt != nil {
		elapsed = snap.StoppedAt.Sub(snap.StartedAt).Round(time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "session %s: %s, %d events in %s\n", snap.ID, snap.State, snap.EventCount, elapsed)
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

func printTraceEvent(e query.Event, format string) {
	if format == "json" {
		data, err := json.Marshal(e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "encode event %d: %v\n", e.Sequence, err)
			return
		}
		fmt.Println(string(data))
		return
	}
	fmt.Println(formatTraceEventLine(e))
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
