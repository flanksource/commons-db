package schedules

import (
	"context"
	"fmt"

	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// Reporter renders a result to report bytes. It is an interface so the schedule
// runner does not depend on the facet renderer directly, and a deployment
// without one still runs its queries.
type Reporter interface {
	Render(ctx dbcontext.Context, spec ReportSpec, result *query.Result, columns []query.ColumnDef) ([]byte, error)
}

// Deliverer sends a produced report to the schedule's channels.
type Deliverer interface {
	Deliver(ctx dbcontext.Context, schedule Schedule, artifact Artifact) ([]DeliveryResult, error)
}

// Artifact is a rendered report on its way to storage and delivery.
type Artifact struct {
	Path        string
	ContentType string
	Filename    string
	Content     []byte
}

// RunnerOptions wire the runner to everything it needs. Reporter, Deliverer and
// Artifacts are optional: without them a schedule still runs its query and
// records the run, it just produces nothing to send.
type RunnerOptions struct {
	Store     StoreProvider
	Profiles  profiles.StoreProvider
	Context   func() dbcontext.Context
	Reconcile *profiles.Service
	Reporter  Reporter
	Deliverer Deliverer
	Artifacts ArtifactStore
}

// Runner executes one schedule inside a clicky task group.
type Runner struct {
	options RunnerOptions
}

func NewRunner(options RunnerOptions) (*Runner, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("schedule runner requires a store")
	}
	if options.Profiles == nil {
		return nil, fmt.Errorf("schedule runner requires a profile store")
	}
	if options.Context == nil {
		return nil, fmt.Errorf("schedule runner requires a context provider")
	}
	return &Runner{options: options}, nil
}

// Register wires the runner into clicky's scheduler for this package's kind.
//
// Registering the same kind twice panics by design in clicky, so this is called
// once at serve time rather than from the constructor, which a test may build
// more than once in a process.
func (r *Runner) Register() {
	task.RegisterRunner(Kind, r.Run)
}

// SetDeliverer installs the notifier once the server knows its own base URL.
func (r *Runner) SetDeliverer(deliverer Deliverer) {
	r.options.Deliverer = deliverer
}

// SetArtifacts installs the artifact store.
func (r *Runner) SetArtifacts(store ArtifactStore) {
	r.options.Artifacts = store
}

// Run is the task.Runner for a schedule. Each stage is its own task in the
// group, so a failure names the stage that failed rather than the schedule.
func (r *Runner) Run(ctx flanksourceContext.Context, timing task.Schedule, group *task.Group) error {
	store, err := r.options.Store()
	if err != nil {
		return err
	}
	schedule, err := store.Get(ctx, timing.Name)
	if err != nil {
		return err
	}

	typed := task.TypedGroup[any]{Group: group}
	typed.Add(schedule.Name, func(taskCtx flanksourceContext.Context, t *task.Task) (any, error) {
		if err := r.execute(taskCtx, t, schedule, group.ID()); err != nil {
			t.Failed()
			return nil, err
		}
		t.Success()
		return nil, nil
	})
	return nil
}

// execute is the whole body of a scheduled run: read, render, store, deliver.
// Each step reports progress onto the task so a watcher sees where it is.
func (r *Runner) execute(ctx flanksourceContext.Context, t *task.Task, schedule Schedule, runID string) error {
	queryCtx := r.options.Context().Wrap(ctx)

	t.SetDescription("running")
	result, columns, err := r.read(queryCtx, schedule)
	if err != nil {
		return err
	}
	t.Infof("%d rows", len(result.Rows))

	if schedule.Report == nil {
		return nil
	}
	if r.options.Reporter == nil {
		return fmt.Errorf("schedule %q asks for a %s report but no renderer is configured",
			schedule.Name, schedule.Report.Format)
	}

	t.SetDescription("rendering")
	rendered, err := r.options.Reporter.Render(queryCtx, *schedule.Report, result, columns)
	if err != nil {
		return fmt.Errorf("render report: %w", err)
	}

	artifact := Artifact{
		ContentType: reportContentType(schedule.Report.Format),
		Filename:    "report" + reportExtension(schedule.Report.Format),
		Content:     rendered,
	}
	if r.options.Artifacts != nil {
		t.SetDescription("storing")
		stored, err := r.options.Artifacts.Save(ctx, schedule.Name, runID, artifact)
		if err != nil {
			return fmt.Errorf("store report: %w", err)
		}
		artifact = stored
		t.Infof("stored %s", artifact.Path)
	}

	var delivered []DeliveryResult
	if len(schedule.Deliver) > 0 {
		if r.options.Deliverer == nil {
			return fmt.Errorf("schedule %q has delivery targets but no notifier is configured", schedule.Name)
		}
		t.SetDescription("delivering")
		delivered, err = r.options.Deliverer.Deliver(queryCtx, schedule, artifact)
		if err != nil {
			return fmt.Errorf("deliver report: %w", err)
		}
	}

	// Recorded even when delivery failed: which channels a report reached is
	// the part of the run someone actually needs to see afterwards.
	store, err := r.options.Store()
	if err != nil {
		return err
	}
	if err := store.RecordReport(ctx, runID, artifact.Path, delivered); err != nil {
		return fmt.Errorf("record report: %w", err)
	}
	for _, result := range delivered {
		if !result.Sent {
			return fmt.Errorf("delivery to %s failed: %s", result.Connection, result.Error)
		}
	}
	return nil
}

// read runs the schedule's query or reconciliation and returns the rows to
// report on, with the columns that describe them. Columns travel separately
// because a reconciliation's are the join's, not either side's profile.
func (r *Runner) read(ctx dbcontext.Context, schedule Schedule) (*query.Result, []query.ColumnDef, error) {
	store, err := r.options.Profiles()
	if err != nil {
		return nil, nil, err
	}

	switch {
	case schedule.Query != nil:
		resolved, err := profiles.Resolve(ctx, store, schedule.Query.Profile)
		if err != nil {
			return nil, nil, err
		}
		params := make(map[string]any, len(schedule.Query.Params))
		for key, value := range schedule.Query.Params {
			params[key] = value
		}
		result, err := query.Execute(ctx, resolved.Profile, params)
		if err != nil {
			return nil, nil, fmt.Errorf("profile %q: %w", schedule.Query.Profile, err)
		}
		return result, resolved.Profile.Columns, nil

	case schedule.Reconcile != nil:
		if r.options.Reconcile == nil {
			return nil, nil, fmt.Errorf(
				"schedule %q reconciles but no profile service is configured", schedule.Name)
		}
		// The same call the manual `reconcile` action makes, so a scheduled
		// reconciliation and a hand-run one cannot drift apart.
		reconciled, err := r.options.Reconcile.Reconcile(
			ctx, schedule.Reconcile.Profile, schedule.Reconcile.ReconcileFlags)
		if err != nil {
			return nil, nil, fmt.Errorf("reconcile %q: %w", schedule.Reconcile.Profile, err)
		}
		result, columns := reconcileResult(reconciled)
		return result, columns, nil

	default:
		return nil, nil, fmt.Errorf("schedule %q runs nothing", schedule.Name)
	}
}

// ArtifactStore persists a rendered report where the run history and the
// delivery link can both reach it.
type ArtifactStore interface {
	Save(ctx context.Context, schedule, runID string, artifact Artifact) (Artifact, error)
}
