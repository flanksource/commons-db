package main

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	commonsContext "github.com/flanksource/commons/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

type topOptions struct {
	interval string
	duration string
	params   []string
}

func newTopCmd() *cobra.Command {
	var o topOptions
	cmd := &cobra.Command{
		Use:   "top <profile>",
		Short: "Sample any profile on an interval and render the latest snapshot as a live table",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTopCmd(cmd, args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.interval, "interval", "", "Sampling interval (default: the profile's top interval, or 5s)")
	f.StringVar(&o.duration, "duration", "", "Stop sampling after this long (default: the profile's maxDuration, capped at 15m)")
	f.StringArrayVar(&o.params, "param", nil, "Profile filter param as key=value (repeatable)")
	return cmd
}

func runTopCmd(cmd *cobra.Command, name string, o topOptions) error {
	p, err := currentStore().Get(name)
	if err != nil {
		return err
	}
	if p.Kind() == query.KindTrace {
		return fmt.Errorf("profile %q is a trace; use `query trace` to stream it", p.Name)
	}
	interval := o.interval
	if p.Top == nil && interval == "" {
		interval = query.DefaultTopInterval.String()
	}
	if p, err = applySessionSpecOverrides(p, interval, o.duration); err != nil {
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

	sigCtx, cancelSig := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelSig()
	go func() {
		<-sigCtx.Done()
		session.Stop()
	}()

	// The clicky task keeps the render loop alive for the session's lifetime;
	// the live renderer swaps the task tree for the latest snapshot table.
	task.SetLiveRenderer(&topLiveRenderer{session: session, profile: p})
	defer task.SetLiveRenderer(nil)
	t := task.StartTask("top "+p.Name, func(_ commonsContext.Context, _ *task.Task) (any, error) {
		_, live, cancel := session.Subscribe()
		defer cancel()
		for range live { // drain until the session reaches a terminal state
		}
		return nil, nil
	})
	_ = t.WaitFor()

	return finishCLISession(session)
}

// topLiveRenderer renders the session's latest snapshot through the clicky
// table formatter on every live frame.
type topLiveRenderer struct {
	session *query.Session
	profile query.Profile
}

func (r *topLiveRenderer) RenderLive(_ []*task.Task) api.Text  { return r.render() }
func (r *topLiveRenderer) RenderFinal(_ []*task.Task) api.Text { return r.render() }

func (r *topLiveRenderer) render() api.Text {
	snap := r.session.Snapshot()
	header := fmt.Sprintf("top %s  every %s  samples=%d  state=%s\n",
		r.profile.Name, r.profile.Top.TickInterval(), snap.EventCount, snap.State)

	latest := r.session.Latest()
	if latest == nil {
		return api.Text{Content: header + "waiting for the first sample…"}
	}
	table, err := latest.Render(r.profile.Columns, "pretty")
	if err != nil {
		return api.Text{Content: header + "render error: " + err.Error()}
	}
	return api.Text{Content: header + table}
}
