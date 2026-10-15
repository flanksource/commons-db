package terminal

import (
	"fmt"
	"time"

	"github.com/flanksource/commons/properties"
)

// Defaults a capture starts from, as gavel's PTY capture settles its screen and
// bounds its cast.
const (
	DefaultWidth            = 120
	DefaultHeight           = 40
	DefaultSnapshotInterval = 100 * time.Millisecond
	DefaultMaxBytes         = "4MiB"

	// maxEvents bounds the output events a capture records; past it, as past
	// MaxBytes, events stop accumulating and the cast says it is truncated.
	maxEvents = 20000
)

// Params describe the terminal a host feeds the capture from.
type Params struct {
	// Width and Height are the terminal's size in cells, and the viewport the
	// settled snapshots are bounded by.
	Width  int `json:"width,omitempty" clicky:"title=Width"`
	Height int `json:"height,omitempty" clicky:"title=Height"`
	// SnapshotInterval, a duration such as 100ms, is how often the settled
	// screen is recorded.
	SnapshotInterval string `json:"snapshotInterval,omitempty" clicky:"title=Snapshot interval"`
	// MaxBytes, a size such as 4MiB, caps the output the capture records as
	// events. The screen goes on settling past it.
	MaxBytes string `json:"maxBytes,omitempty" clicky:"title=Max bytes"`
	// Command is the command line the terminal runs, for the cast's header.
	Command []string `json:"command,omitempty" clicky:"title=Command"`
}

func (p Params) Validate() error {
	if p.Width <= 0 || p.Height <= 0 {
		return fmt.Errorf("width and height must be > 0 (got %dx%d)", p.Width, p.Height)
	}
	if _, err := p.interval(); err != nil {
		return err
	}
	_, err := p.byteCap()
	return err
}

func (p Params) interval() (time.Duration, error) {
	interval, err := time.ParseDuration(p.SnapshotInterval)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("snapshotInterval %q is not a positive duration such as 100ms", p.SnapshotInterval)
	}
	return interval, nil
}

func (p Params) byteCap() (int64, error) {
	size, err := properties.ParseBytes(p.MaxBytes)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("maxBytes %q is not a positive size such as 4MiB", p.MaxBytes)
	}
	return size, nil
}
