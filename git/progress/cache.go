package progress

import (
	"context"
	"strings"
	"sync"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/readcache"
)

func WithTasks(ctx context.Context, options TaskOptions) context.Context {
	return readcache.WithObserver(git.WithReadObserver(git.WithFetchObserver(ctx, Tasks(options)), Reads(options)), CacheTasks(options))
}

func CacheTasks(options TaskOptions) readcache.Observer {
	var mu sync.Mutex
	steps := map[string]*task.Step{}
	return func(ctx context.Context, event readcache.Event) {
		group := task.GroupFromContext(ctx)
		if group == nil && options.Group != nil {
			if supplied := options.Group(ctx); supplied != nil {
				group = supplied
			}
		}
		if group == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		step := steps[event.ID]
		if step == nil {
			name := strings.Title(event.Operation)
			if event.Operation == "" {
				name = "Cache lookup"
			}
			step = group.StartStep(taskName(event.Repository, name, event.Ref), task.WithContext(ctx))
			step.Task().SetLabels(map[string]string{"repository": repositoryName(event.Repository), "ref": event.Ref, "operation": event.Operation})
			steps[event.ID] = step
		}
		description := "Loading source"
		if event.Disposition == "lookup" {
			description = "Checking cache"
		}
		if event.Disposition == "hit" {
			description = "Using cached result"
		}
		if event.Disposition == "joined" {
			description = "Waiting for shared read"
		}
		step.Task().SetDescription(description)
		if event.Done {
			step.Finish(event.Err)
			delete(steps, event.ID)
		}
	}
}
