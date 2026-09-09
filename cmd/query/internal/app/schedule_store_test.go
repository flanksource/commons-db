package app

import (
	"testing"
	"time"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/cmd/query/schedules"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/require"
)

// scheduleStoreForT brings up a migrated database and returns a store over it.
// It lives in this package because the migration bundle is embedded here, and a
// store test that invented its own DDL would stop testing the real schema.
func scheduleStoreForT(t *testing.T) *schedules.Store {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "query_schedules", LogName: "query-schedules-test"})
	require.NoError(t, migrateSchema(t.Context(), handle.DSN()))

	store, err := schedules.NewStore(handle.Gorm())
	require.NoError(t, err)
	return store
}

func nightly() schedules.Schedule {
	return schedules.Schedule{
		Name:    "nightly",
		Cron:    "0 6 * * *",
		Enabled: true,
		Query:   &schedules.QuerySpec{Profile: "orders"},
		Labels:  map[string]string{"team": "ops"},
	}
}

func TestScheduleStoreRoundTrip(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.Save(ctx, nightly()))

	stored, err := store.Get(ctx, "nightly")
	require.NoError(t, err)
	require.Equal(t, "0 6 * * *", stored.Cron)
	require.True(t, stored.Enabled)
	require.NotNil(t, stored.Query)
	require.Equal(t, "orders", stored.Query.Profile)
	require.Equal(t, map[string]string{"team": "ops"}, stored.Labels)

	// Saving the same name again updates rather than duplicating: a schedule is
	// addressed by its name, so two rows for one name would make "which one
	// runs?" unanswerable.
	updated := nightly()
	updated.Cron = "0 7 * * *"
	require.NoError(t, store.Save(ctx, updated))

	all, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "0 7 * * *", all[0].Cron)
}

func TestScheduleStoreRefusesAnUnrunnableSchedule(t *testing.T) {
	store := scheduleStoreForT(t)

	broken := nightly()
	broken.Cron = "whenever"
	require.Error(t, store.Save(t.Context(), broken))

	_, err := store.Get(t.Context(), "nightly")
	require.ErrorIs(t, err, schedules.ErrNotFound, "an invalid schedule must not be persisted at all")
}

// The enabled flag and the fire times are columns, not part of the spec blob, so
// pausing a schedule cannot be undone by a later edit that carries a stale copy.
func TestScheduleStoreEnabledIsAColumnNotPartOfTheSpec(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.Save(ctx, nightly()))
	require.NoError(t, store.SetEnabled(ctx, "nightly", false))

	paused, err := store.Get(ctx, "nightly")
	require.NoError(t, err)
	require.False(t, paused.Enabled)

	require.NoError(t, store.SetEnabled(ctx, "nightly", true))
	resumed, err := store.Get(ctx, "nightly")
	require.NoError(t, err)
	require.True(t, resumed.Enabled)
}

func TestScheduleStoreDeleteIsNotFoundTheSecondTime(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.Save(ctx, nightly()))
	require.NoError(t, store.Delete(ctx, "nightly"))
	require.ErrorIs(t, store.Delete(ctx, "nightly"), schedules.ErrNotFound)
}

// snapshotsFor builds the group-plus-tasks slice the task manager hands a store.
func snapshotsFor(id, schedule, status string) []task.TaskSnapshot {
	started := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	return []task.TaskSnapshot{
		{
			ID: schedule, Name: schedule, Type: "group", GroupID: id, Status: status,
			Kind: schedules.Kind, Labels: map[string]string{"schedule": schedule},
			Total: 1, Completed: 1, StartedAt: started, FinishedAt: finished,
		},
		{ID: id + "-task", Name: "work", Type: "task", GroupID: id, Status: status},
	}
}

func TestScheduleStoreSaveRunIsIdempotent(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	// The task manager writes a run twice by design: once when it finishes and
	// again if it is still in memory when GC evicts it. Both must land on one
	// row, or every run would be double-counted in its own history.
	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "running")))
	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "success")))

	runs, err := store.ListRuns(ctx, schedules.RunFilter{})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "success", runs[0].Status, "the newer snapshot wins")
	require.Equal(t, "nightly", runs[0].Schedule)
	require.Equal(t, 1, runs[0].Completed)
}

func TestScheduleStoreRunFiltering(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "success")))
	require.NoError(t, store.SaveRun(ctx, "run-2", snapshotsFor("run-2", "hourly", "failed")))

	byName, err := store.ListRuns(ctx, schedules.RunFilter{Schedule: "nightly"})
	require.NoError(t, err)
	require.Len(t, byName, 1)
	require.Equal(t, "run-1", byName[0].ID)

	byStatus, err := store.ListRuns(ctx, schedules.RunFilter{Status: "failed"})
	require.NoError(t, err)
	require.Len(t, byStatus, 1)
	require.Equal(t, "run-2", byStatus[0].ID)

	// The label filter is what the clicky task API narrows on.
	byLabel, err := store.Runs(ctx, task.RunFilter{Labels: map[string]string{"schedule": "hourly"}})
	require.NoError(t, err)
	require.Len(t, byLabel, 1)
	require.Equal(t, "run-2", byLabel[0].ID)
}

// A run's snapshots have to survive a restart, or a drill-down into yesterday's
// failure shows nothing.
func TestScheduleStoreSnapshotsSurviveTheProcess(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "success")))

	snapshots, err := store.Snapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	require.Equal(t, "group", snapshots[0].Type)
	require.Equal(t, "run-1", snapshots[0].GroupID)
	require.Equal(t, "task", snapshots[1].Type)

	missing, err := store.Snapshot(ctx, "never-ran")
	require.NoError(t, err)
	require.Nil(t, missing)
}

// The report is recorded after the run's own snapshots are written, so the
// snapshot write must not blank the artifact back out.
func TestScheduleStoreRecordReportSurvivesALaterSnapshotWrite(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "running")))
	require.NoError(t, store.RecordReport(ctx, "run-1", "artifacts/nightly/run-1/report.pdf",
		[]schedules.DeliveryResult{{Connection: "ops-email", Channel: "email", Sent: true, Attached: true}}))

	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "success")))

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, "success", run.Status)
	require.Equal(t, "artifacts/nightly/run-1/report.pdf", run.ArtifactPath)
	require.Len(t, run.Delivery, 1)
	require.True(t, run.Delivery[0].Sent)
	require.True(t, run.Delivery[0].Attached)
}

// The report is recorded from inside the run's own task, which is necessarily
// before the run itself is persisted — a run is only written once it is
// terminal, and it cannot be terminal while that task is still going. A plain
// UPDATE here matches no row and throws the artifact away silently.
func TestScheduleStoreRecordReportBeforeTheRunExists(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.RecordReport(ctx, "run-1", "artifacts/nightly/run-1/report.csv",
		[]schedules.DeliveryResult{{Connection: "ops-slack", Channel: "slack", Sent: true}}))

	// The run then finishes and lands on top of the placeholder.
	require.NoError(t, store.SaveRun(ctx, "run-1", snapshotsFor("run-1", "nightly", "success")))

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, "success", run.Status, "the finished run completes the placeholder")
	require.Equal(t, "nightly", run.Schedule)
	require.Equal(t, "artifacts/nightly/run-1/report.csv", run.ArtifactPath,
		"the artifact recorded before the run must survive the run landing")
	require.Len(t, run.Delivery, 1)
	require.True(t, run.Delivery[0].Sent)
}

func TestScheduleStoreRecordsSkippedFires(t *testing.T) {
	store := scheduleStoreForT(t)
	ctx := t.Context()

	require.NoError(t, store.Save(ctx, nightly()))

	scheduled := time.Now().Add(-time.Hour)
	// A fire that produced no run is still a fact about the schedule: without
	// it, a schedule that keeps skipping looks identical to one that is idle.
	require.NoError(t, store.RecordFire(ctx, "nightly", task.Fire{
		ScheduledFor: scheduled, At: time.Now(), Outcome: task.FireSkipped,
		RunID: "run-0", Reason: "previous run still in progress",
	}))
	require.NoError(t, store.RecordFire(ctx, "nightly", task.Fire{
		ScheduledFor: scheduled, At: time.Now(), Outcome: task.FireStarted, RunID: "run-1",
	}))
}

// A finished run has no goroutine left to control, and reporting success for an
// action that did nothing is worse than refusing it.
func TestScheduleStoreRefusesToControlAFinishedRun(t *testing.T) {
	store := scheduleStoreForT(t)
	require.Error(t, store.Control(t.Context(), "run-1", task.ControlStop))
}
