package shell

import (
	"fmt"
	"time"

	clickyexec "github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons-db/context"
)

func (c commandContext) display() (string, []string) {
	path := c.displayPath
	if path == "" {
		path = redactText(c.cmd.Path, c.sensitiveValues)
	}
	args := c.displayArgs
	if args == nil {
		args = c.cmd.Args
	}
	redacted := make([]string, len(args))
	for index, arg := range args {
		redacted[index] = redactText(arg, c.sensitiveValues)
	}
	return path, redacted
}

func runProcess(ctx context.Context, process *clickyexec.Process) (*clickyexec.ExecResult, error) {
	done := make(chan *clickyexec.Process, 1)
	go func() { done <- process.Run() }()

	select {
	case completed := <-done:
		result := completed.Result()
		if result.ExitCode < 0 {
			return result, fmt.Errorf("command execution failed")
		}
		return result, nil
	case <-ctx.Done():
		completed := killStartedProcess(process, done)
		return completed.Result(), ctx.Err()
	}
}

func killStartedProcess(process *clickyexec.Process, done <-chan *clickyexec.Process) *clickyexec.Process {
	for process.Pid() == 0 {
		select {
		case completed := <-done:
			return completed
		case <-time.After(time.Millisecond):
		}
	}
	_ = process.KillTree()
	return <-done
}

func successCode(code int, configured []int) bool {
	if len(configured) == 0 {
		return code == 0
	}
	for _, allowed := range configured {
		if code == allowed {
			return true
		}
	}
	return false
}

func environmentMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := splitEnv(item)
		if ok {
			values[key] = value
		}
	}
	return values
}
