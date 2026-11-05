package progress

import (
	"context"
	"sync"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git"
)

type TaskOptions struct {
	Group func(context.Context) *task.Group
}

// Tasks records inline fetch steps without occupying a worker while Git runs.
func Tasks(options TaskOptions) git.FetchObserver {
	var mu sync.Mutex
	steps := map[string]*task.Step{}
	return func(ctx context.Context, event git.FetchProgress) {
		if event.ID == "" {
			panic("git progress: operation ID is required")
		}
		mu.Lock()
		defer mu.Unlock()
		step := steps[event.ID]
		if step == nil {
			group := task.GroupFromContext(ctx)
			if group == nil && options.Group != nil {
				group = options.Group(ctx)
			}
			if group == nil {
				group = task.StartGroup[any](taskName(event.Repository, "Fetch", event.Ref), task.WithGroupContext(ctx), task.WithLabels(map[string]string{"repository": repositoryName(event.Repository)})).Group
			}
			step = group.StartStep(fetchTaskName(event), task.WithContext(ctx))
			labels := map[string]string{"repository": repositoryName(event.Repository), "ref": event.Ref, "operation": "fetch"}
			if event.Mirror != "" {
				labels["mirror"] = event.Mirror
			}
			step.Task().SetLabels(labels)
			steps[event.ID] = step
		}
		if event.Done {
			step.Finish(event.Err)
			delete(steps, event.ID)
			return
		}
		step.Task().SetDescription(fetchProgressLine(event))
		step.Task().SetProgress(int(event.Current), int(event.Total))
		unit := "objects"
		if event.Phase == "Resolving deltas" {
			unit = "deltas"
		}
		step.Task().SetProgressMetrics(task.ProgressMetrics{Unit: unit, Bytes: event.Bytes, BytesPerSecond: event.BytesPerSecond})
	}
}

// fetchTaskName names the fetch, and the other repository's mirror it writes
// into when the mirror is shared.
func fetchTaskName(progress git.FetchProgress) string {
	name := taskName(progress.Repository, "Fetch", progress.Ref)
	switch {
	case progress.MirrorRepository != "":
		return name + " → " + repositoryName(progress.MirrorRepository)
	case progress.Mirror != "":
		return name + " → " + progress.Mirror
	}
	return name
}

func fetchProgressLine(progress git.FetchProgress) string {
	return progress.Phase
}
