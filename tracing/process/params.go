// Package process holds the process trace kinds: process_tree records each
// process under a root as it starts, changes and exits, and process_samples
// records the CPU and memory of every one of them at each tick.
package process

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// MinInterval is the shortest tick a capture takes: each tick lists every
// process on the host.
const MinInterval = 250 * time.Millisecond

const defaultInterval = time.Second

// Params choose the process tree a capture follows and how often it looks.
type Params struct {
	// RootPID is the process whose tree is captured; zero is the host's own.
	RootPID int `json:"rootPid,omitempty" clicky:"title=Root PID"`
	// Interval, a duration such as 1s, is how often the tree is sampled.
	// Empty is a second.
	Interval string `json:"interval,omitempty" clicky:"title=Interval"`
}

func (p Params) Validate() error {
	if p.RootPID < 0 {
		return fmt.Errorf("rootPid %d is not a process id", p.RootPID)
	}
	_, err := p.interval()
	return err
}

func (p Params) interval() (time.Duration, error) {
	if p.Interval == "" {
		return defaultInterval, nil
	}
	interval, err := time.ParseDuration(p.Interval)
	if err != nil {
		return 0, fmt.Errorf("interval %q is not a duration such as 1s", p.Interval)
	}
	if interval < MinInterval {
		return 0, fmt.Errorf("interval %s is shorter than %s", interval, MinInterval)
	}
	return interval, nil
}

// root is the process the capture follows.
func (p Params) root() int {
	if p.RootPID == 0 {
		return os.Getpid()
	}
	return p.RootPID
}

// errRootGone ends a capture whose root has exited.
var errRootGone = errors.New("root process exited")
