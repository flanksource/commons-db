package sessions

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/query"
	commonsContext "github.com/flanksource/commons/context"
)

type TopOptions struct {
	Interval string
	Duration string
	Params   []string
}

func (r *Runner) RunTop(ctx context.Context, name string, options TopOptions) error {
	store, err := r.profiles()
	if err != nil {
		return err
	}
	p, err := store.Get(ctx, name)
	if err != nil {
		return err
	}
	if p.Kind() == query.KindTrace {
		return fmt.Errorf("profile %q is a trace; use `query trace` to stream it", p.Name)
	}
	interval := options.Interval
	if p.Top == nil && interval == "" {
		interval = query.DefaultTopInterval.String()
	}
	if p, err = applySessionSpecOverrides(p, interval, options.Duration); err != nil {
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

	sigCtx, cancelSig := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
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

	return r.finishCLISession(session)
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
