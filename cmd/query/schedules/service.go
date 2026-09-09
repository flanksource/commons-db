package schedules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
)

// StoreProvider resolves the store lazily, because the command tree is built
// before the database exists — a metadata-only invocation must not start
// PostgreSQL just to list its own subcommands.
type StoreProvider func() (*Store, error)

// Options wire the service to its store and the scheduler it drives.
type Options struct {
	Store      StoreProvider
	Scheduler  *task.Scheduler
	DecodeBody profiles.BodyDecoder

	// Runner and Deliverer power the trigger and test-delivery actions. Both
	// are optional; without them those actions refuse rather than pretend.
	Runner    *Runner
	Deliverer Deliverer

	// Context supplies the database-backed context delivery needs to resolve
	// connections and their secrets.
	Context func() dbcontext.Context
}

// Service is the CLI and HTTP surface over schedules and their run history.
type Service struct {
	store      StoreProvider
	scheduler  *task.Scheduler
	decodeBody profiles.BodyDecoder
	runner     *Runner
	deliverer  Deliverer
	context    func() dbcontext.Context
}

func New(options Options) (*Service, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("schedule service requires a store provider")
	}
	if options.DecodeBody == nil {
		return nil, fmt.Errorf("schedule service requires a body decoder")
	}
	return &Service{
		store:      options.Store,
		scheduler:  options.Scheduler,
		decodeBody: options.DecodeBody,
		runner:     options.Runner,
		deliverer:  options.Deliverer,
		context:    options.Context,
	}, nil
}

// SetDeliverer installs the notifier. It is set at serve time rather than in
// the constructor because delivery needs the externally reachable base URL,
// which only the server knows.
func (s *Service) SetDeliverer(deliverer Deliverer) {
	s.deliverer = deliverer
	if s.runner != nil {
		s.runner.SetDeliverer(deliverer)
	}
}

// listOptions are the schedule listing's filters.
type listOptions struct {
	Enabled string `flag:"enabled" help:"Only schedules that are enabled or disabled"`
}

// runListOptions are the run listing's filters. Schedule is the one that makes
// this a per-schedule history rather than a global one.
// The filter flag is deliberately not called `schedule`: clicky promotes a flag
// named after the parent command into a path parameter, which would generate
// GET /api/v1/schedule/{schedule}/run and collide with the run entity's own
// /api/v1/schedule/run/{id}.
type runListOptions struct {
	Of     string `flag:"of" help:"Only runs of this schedule"`
	Status string `flag:"status" help:"Only runs with this status"`
	Limit  int    `flag:"limit" help:"Maximum runs to return"`
}

// RegisterClicky registers the schedule entity and, nested under it, the run
// entity that is its history.
//
// Runs are an entity rather than an action returning a blob, so they arrive with
// filtering, sorting and paging already built. clicky derives the REST path from
// the command nesting, so `query schedule run list` and GET /api/v1/schedule/run
// come from the same declaration.
//
// The consequence is that the fire-now action cannot be called `run` — it would
// collide with the child command — so it is `trigger`.
func (s *Service) RegisterClicky() {
	clicky.NewEntity[Schedule, listOptions, Schedule]("schedule").
		Aliases("schedules").
		ListWithContext(s.list).
		GetWithContext(s.get).
		CreateWithContext(func(ctx context.Context, body map[string]any) (Schedule, error) {
			return s.Save(ctx, body, "")
		}).
		UpdateWithContext(s.update).
		DeleteWithContext(s.Delete).
		WithAction(entity.TypedActionWithContext("trigger", TriggerFlags{}, s.Trigger).
			WithShort("Run this schedule now, without waiting for its next scheduled time")).
		WithAction(entity.TypedActionWithContext("pause", PauseFlags{}, s.Pause).
			WithShort("Stop firing this schedule, keeping its definition and history")).
		WithAction(entity.TypedActionWithContext("resume", PauseFlags{}, s.Resume).
			WithShort("Start firing this schedule again")).
		WithAction(entity.TypedActionWithContext("test-delivery", TestDeliveryFlags{}, s.TestDelivery).
			WithShort("Send a sample message to this schedule's delivery channels").
			// This sends real messages to real channels, so it asks even where
			// the app's default policy would not.
			WithToolPermission(entity.ToolPermissionAsk)).
		Register()

	clicky.NewEntity[Run, runListOptions, Run]("run").
		Parent("schedule").
		ListWithContext(s.listRuns).
		GetWithContext(s.getRun).
		WithAction(entity.ActionWithFlagsAndContext("snapshots", SnapshotFlags{}, s.runSnapshots).
			WithShort("Read the full task snapshots recorded for this run").
			// A stored run never changes, so its snapshots are a bookmarkable
			// GET rather than the POST an action defaults to.
			WithMethod(http.MethodGet)).
		Register()
}

func (s *Service) list(ctx context.Context, options listOptions) ([]Schedule, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	schedules, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	if options.Enabled == "" {
		return schedules, nil
	}
	want := options.Enabled == "true"
	filtered := make([]Schedule, 0, len(schedules))
	for _, schedule := range schedules {
		if schedule.Enabled == want {
			filtered = append(filtered, schedule)
		}
	}
	return filtered, nil
}

func (s *Service) get(ctx context.Context, name string) (Schedule, error) {
	store, err := s.store()
	if err != nil {
		return Schedule{}, err
	}
	return store.Get(ctx, name)
}

func (s *Service) update(ctx context.Context, id string, body map[string]any) (Schedule, error) {
	return s.Save(ctx, body, id)
}

func (s *Service) listRuns(ctx context.Context, options runListOptions) ([]Run, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	// Scoped to this kind because the task store is global: it also holds the
	// scheduler's own supervisory run, which is not a schedule's history.
	return store.ListRuns(ctx, RunFilter{
		Kind:     Kind,
		Schedule: options.Of,
		Status:   options.Status,
		Limit:    options.Limit,
	})
}

func (s *Service) getRun(ctx context.Context, id string) (Run, error) {
	store, err := s.store()
	if err != nil {
		return Run{}, err
	}
	return store.GetRun(ctx, id)
}

func (s *Service) runSnapshots(ctx context.Context, id string, _ map[string]string) ([]task.TaskSnapshot, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return store.Snapshot(ctx, id)
}

// Save decodes a schedule document and persists it, then re-registers it with
// the scheduler so an edit takes effect without a restart.
func (s *Service) Save(ctx context.Context, body map[string]any, id string) (Schedule, error) {
	store, err := s.store()
	if err != nil {
		return Schedule{}, err
	}
	schedule, err := s.decode(ctx, body, id)
	if err != nil {
		return Schedule{}, err
	}
	if err := store.Save(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	if err := s.sync(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	return store.Get(ctx, schedule.Name)
}

// decode turns a request body into a schedule, reconciling the name in the
// document with the one in the path.
func (s *Service) decode(ctx context.Context, body map[string]any, id string) (Schedule, error) {
	decoded, err := s.decodeBody(ctx, body)
	if err != nil {
		return Schedule{}, err
	}
	if err := coerceScalars(decoded); err != nil {
		return Schedule{}, err
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return Schedule{}, fmt.Errorf("encode schedule: %w", err)
	}
	var schedule Schedule
	if err := json.Unmarshal(encoded, &schedule); err != nil {
		return Schedule{}, fmt.Errorf("decode schedule: %w", err)
	}
	if schedule.Name == "" {
		schedule.Name = id
	}
	if id != "" && schedule.Name != id {
		return Schedule{}, fmt.Errorf("schedule name %q does not match %q", schedule.Name, id)
	}
	return schedule, nil
}

// coerceScalars converts the string values the CLI's `key=value` form produces
// into the types the document declares.
//
// A JSON body already arrives correctly typed; `query schedule create
// enabled=true` cannot, because the shell has no types. Rejecting the most
// obvious invocation of the command over that is not a defensible boundary.
func coerceScalars(body map[string]any) error {
	raw, ok := body["enabled"]
	if !ok {
		return nil
	}
	text, isString := raw.(string)
	if !isString {
		return nil
	}
	parsed, err := strconv.ParseBool(text)
	if err != nil {
		return fmt.Errorf("enabled must be true or false, got %q", text)
	}
	body["enabled"] = parsed
	return nil
}

// Delete removes a schedule and stops firing it.
func (s *Service) Delete(ctx context.Context, name string) error {
	store, err := s.store()
	if err != nil {
		return err
	}
	if err := store.Delete(ctx, name); err != nil {
		return err
	}
	if s.scheduler != nil {
		return s.scheduler.Remove(ctx, name)
	}
	return nil
}

// LoadScheduler registers every stored schedule with the scheduler. It is called
// at startup, once the database is up.
func (s *Service) LoadScheduler(ctx context.Context) error {
	if s.scheduler == nil {
		return nil
	}
	store, err := s.store()
	if err != nil {
		return err
	}
	schedules, err := store.List(ctx)
	if err != nil {
		return err
	}
	var failures []string
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		if err := s.scheduler.Add(ctx, schedule.Timing()); err != nil {
			// One unusable schedule must not stop the rest from being loaded,
			// but it must be said out loud rather than dropped.
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("load schedules: %v", failures)
	}
	return nil
}

// sync registers the schedule's current timing with the scheduler, or stops
// firing it when it is disabled.
func (s *Service) sync(ctx context.Context, schedule Schedule) error {
	if s.scheduler == nil {
		return nil
	}
	if !schedule.Enabled {
		return s.scheduler.Remove(ctx, schedule.Name)
	}
	return s.scheduler.Add(ctx, schedule.Timing())
}
