package progress

import (
	"context"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git"
	"sync"
)

func Reads(options TaskOptions) git.ReadObserver {
	var mu sync.Mutex
	steps := map[string]*task.Step{}
	return func(ctx context.Context, event git.ReadProgress) {
		mu.Lock()
		defer mu.Unlock()
		step := steps[event.ID]
		if step == nil {
			group := task.GroupFromContext(ctx)
			if group == nil && options.Group != nil {
				group = options.Group(ctx)
			}
			if group == nil {
				group = task.StartGroup[any]("["+repositoryName(event.Repository)+"] Read objects", task.WithGroupContext(ctx)).Group
			}
			step = group.StartStep(taskName(event.Repository, "Read objects", event.Ref), task.WithContext(ctx))
			step.Task().SetLabels(map[string]string{"repository": repositoryName(event.Repository), "ref": event.Ref, "operation": "cat-file"})
			steps[event.ID] = step
		}
		step.Task().SetDescription("Reading objects")
		step.Task().SetProgress(int(event.Current), int(event.Total))
		step.Task().SetProgressMetrics(task.ProgressMetrics{Unit: "objects", Bytes: event.Bytes, BytesPerSecond: event.BytesPerSecond})
		if event.Done {
			step.Finish(event.Err)
			delete(steps, event.ID)
		}
	}
}
