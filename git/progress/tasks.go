package progress

import (
	"context"
	"fmt"
	"strings"
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
				group = task.StartGroup[any]("Git", task.WithGroupContext(ctx)).Group
			}
			step = group.StartStep(fetchTaskName(event), task.WithContext(ctx))
			steps[event.ID] = step
		}
		if event.Done {
			step.Finish(event.Err)
			delete(steps, event.ID)
			return
		}
		step.Task().SetDescription(fetchProgressLine(event))
		if event.Total > 0 {
			step.Task().SetProgress(int(event.Current), int(event.Total))
		}
		if event.PhaseDone {
			step.Task().Infof("%s", fetchProgressLine(event))
		}
	}
}

func fetchTaskName(progress git.FetchProgress) string {
	segments := strings.Split(strings.TrimSuffix(strings.TrimRight(progress.Repository, "/"), ".git"), "/")
	repository := segments[len(segments)-1]
	if len(segments) > 1 && segments[len(segments)-2] != "" {
		repository = segments[len(segments)-2] + "/" + repository
	}
	if ref, err := (git.RemoteRefs{}).Resolve(progress.Ref); err == nil && ref.Kind == git.RefSHA {
		return fmt.Sprintf("git fetch %s@%s", repository, progress.Ref[:12])
	}
	return fmt.Sprintf("git fetch %s#%s", repository, progress.Ref)
}

func fetchProgressLine(progress git.FetchProgress) string {
	var line strings.Builder
	if progress.Remote {
		line.WriteString("remote: ")
	}
	line.WriteString(progress.Phase)
	if progress.Total > 0 {
		fmt.Fprintf(&line, " %3d%% (%d/%d)", progress.Percent, progress.Current, progress.Total)
	} else if progress.Current > 0 {
		fmt.Fprintf(&line, " %d", progress.Current)
	}
	if progress.Transferred != "" {
		fmt.Fprintf(&line, ", %s | %s", progress.Transferred, progress.Rate)
	}
	if progress.PhaseDone {
		line.WriteString(", done")
	}
	return line.String()
}
