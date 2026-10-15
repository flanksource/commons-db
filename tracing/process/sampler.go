package process

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
)

// reuseWithin is how old a listing a capture may be handed instead of a new
// one: shorter than MinInterval, so no capture ever sees one listing twice.
const reuseWithin = 200 * time.Millisecond

// Sampler lists the host's processes for the captures that share it, so the
// tree and samples captures of one root cost one listing per tick.
type Sampler struct {
	mu       sync.Mutex
	listed   *clickyprocess.Snapshot
	listedAt time.Time
}

func NewSampler() *Sampler { return &Sampler{} }

// Subtree is root and every process under it at a listing taken within
// reuseWithin of now, and when that listing was taken. A root that is not
// running returns no processes.
func (s *Sampler) Subtree(ctx context.Context, root int) ([]clickyprocess.Process, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listed == nil || time.Since(s.listedAt) >= reuseWithin {
		listed, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{})
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("list processes: %w", err)
		}
		s.listed, s.listedAt = listed, time.Now().UTC()
	}
	return s.listed.Subtree(root), s.listedAt, nil
}

// Key identifies a process across ticks, and tells apart two processes that
// held one pid: its pid and the second it started. A process whose start is
// unknown is keyed by its pid alone.
func Key(p clickyprocess.Process) string {
	if p.StartedAt == nil {
		return strconv.Itoa(p.PID)
	}
	return strconv.Itoa(p.PID) + "@" + strconv.FormatInt(p.StartedAt.Unix(), 10)
}

// name is the base name of the program p runs.
func name(command string) string {
	program, _, _ := strings.Cut(strings.TrimSpace(command), " ")
	return filepath.Base(program)
}
