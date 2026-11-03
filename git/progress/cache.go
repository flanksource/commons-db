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
	return readcache.WithObserver(git.WithFetchObserver(ctx, Tasks(options)), CacheTasks(options))
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
			name := "Git " + event.Operation
			if event.Operation == "" {
				name = "Git cache lookup"
			}
			if event.Repository != "" {
				name += " " + event.Repository
			}
			if event.Ref != "" {
				name += "#" + event.Ref
			}
			step = group.StartStep(strings.TrimSpace(name), task.WithContext(ctx))
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
