package schedules

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
)

// TriggerFlags are the flags of the schedule `trigger` action.
type TriggerFlags struct {
	Wait bool `flag:"wait" help:"Block until the run finishes instead of returning its id"`
}

func (TriggerFlags) ClickyActionFlags() {}

// PauseFlags carries no options; pausing is the whole instruction.
type PauseFlags struct{}

func (PauseFlags) ClickyActionFlags() {}

// TestDeliveryFlags are the flags of the `test-delivery` action.
type TestDeliveryFlags struct {
	Message string `flag:"message" help:"Body of the test message"`
}

func (TestDeliveryFlags) ClickyActionFlags() {}

// SnapshotFlags carries no options.
type SnapshotFlags struct{}

func (SnapshotFlags) ClickyActionFlags() {}

// TriggerResult names the run a trigger started.
type TriggerResult struct {
	Schedule string     `json:"schedule" pretty:"label=Schedule"`
	RunID    string     `json:"runId" pretty:"label=Run"`
	Status   string     `json:"status" pretty:"label=Status"`
	Rows     int        `json:"rows,omitempty" pretty:"label=Rows"`
	Artifact string     `json:"artifact,omitempty" pretty:"label=Artifact"`
	Error    string     `json:"error,omitempty" pretty:"label=Error"`
	Started  time.Time  `json:"startedAt" pretty:"label=Started"`
	Finished *time.Time `json:"finishedAt,omitempty" pretty:"label=Finished"`
}

func (r TriggerResult) Pretty() api.Text {
	text := api.Text{Content: fmt.Sprintf("%s %s", r.Schedule, r.Status)}
	if r.Error != "" {
		return text.Append(": "+r.Error, "text-red-500")
	}
	return text
}

// Trigger runs a schedule now. It goes through the same runner the scheduler
// uses, so a manual run and a scheduled one are the same code path and produce
// the same history — a "run it now" that behaved differently would be worse than
// no button at all.
func (s *Service) Trigger(ctx context.Context, name string, options TriggerFlags) (*TriggerResult, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	schedule, err := store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if s.runner == nil {
		return nil, fmt.Errorf("schedule %q cannot be triggered: no runner is configured", name)
	}

	group := task.StartGroup[any](schedule.Name,
		task.WithKind(Kind),
		task.WithLabels(map[string]string{"schedule": schedule.Name, "trigger": "manual"}),
		task.WithOwner(schedule.Owner),
	)
	started := time.Now()

	runCtx := flanksourceContext.NewContext(ctx)
	if err := s.runner.Run(runCtx, schedule.Timing(), group.Group); err != nil {
		return nil, err
	}

	result := &TriggerResult{
		Schedule: schedule.Name,
		RunID:    group.ID(),
		Status:   string(task.StatusRunning),
		Started:  started,
	}
	if !options.Wait {
		return result, nil
	}

	group.WaitFor()
	stored, err := store.GetRun(ctx, group.ID())
	if err != nil {
		// The run happened; only the record of it is missing, and saying so
		// beats reporting a failure that did not occur.
		result.Status = string(group.Status())
		return result, nil
	}
	result.Status = stored.Status
	result.Artifact = stored.ArtifactPath
	result.Finished = stored.FinishedAt
	return result, nil
}

// Pause stops a schedule firing without deleting it or its history.
func (s *Service) Pause(ctx context.Context, name string, _ PauseFlags) (Schedule, error) {
	return s.setEnabled(ctx, name, false)
}

// Resume starts a paused schedule firing again.
func (s *Service) Resume(ctx context.Context, name string, _ PauseFlags) (Schedule, error) {
	return s.setEnabled(ctx, name, true)
}

func (s *Service) setEnabled(ctx context.Context, name string, enabled bool) (Schedule, error) {
	store, err := s.store()
	if err != nil {
		return Schedule{}, err
	}
	if err := store.SetEnabled(ctx, name, enabled); err != nil {
		return Schedule{}, err
	}
	schedule, err := store.Get(ctx, name)
	if err != nil {
		return Schedule{}, err
	}
	if err := s.sync(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

// TestDelivery sends a sample message to each of a schedule's channels, so a
// misconfigured connection is found deliberately rather than by a report going
// missing at 6am.
func (s *Service) TestDelivery(
	ctx context.Context,
	name string,
	options TestDeliveryFlags,
) ([]DeliveryResult, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	schedule, err := store.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(schedule.Deliver) == 0 {
		return nil, fmt.Errorf("schedule %q has no delivery targets", name)
	}
	if s.deliverer == nil || s.context == nil {
		return nil, fmt.Errorf("schedule %q cannot be tested: no notifier is configured", name)
	}

	message := options.Message
	if message == "" {
		message = fmt.Sprintf("Test message from the %q schedule.", name)
	}
	sample := Artifact{
		ContentType: "text/plain",
		Filename:    "sample.txt",
		Content:     []byte(message),
	}
	// The sample carries no stored path, so delivery links resolve to nothing
	// and each channel exercises only what a test can honestly exercise: that
	// the connection resolves and the send succeeds.
	probe := schedule
	probe.Report = &ReportSpec{Title: "Test message", Format: "markdown"}
	for i := range probe.Deliver {
		probe.Deliver[i].Message = message
		probe.Deliver[i].Attach = false
	}
	return s.deliverer.Deliver(s.context().Wrap(ctx), probe, sample)
}
