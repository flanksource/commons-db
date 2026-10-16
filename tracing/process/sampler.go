package process

import (
	"context"
	"errors"
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

// cpuWindow is the shortest gap a CPU% is rated over: linux ps reports CPU
// time in whole seconds.
const cpuWindow = time.Second

// Sampler lists the host's processes for the captures that share it, so the
// tree and samples captures of one root cost one listing per tick.
type Sampler struct {
	mu       sync.Mutex
	listed   *clickyprocess.Snapshot
	listedAt time.Time
	rates    *cpuRates
}

func NewSampler() *Sampler { return &Sampler{rates: newCPURates()} }

// observed is a process at a listing and the CPU% it used over its latest
// window of at least cpuWindow; nil until that window has passed.
type observed struct {
	clickyprocess.Process
	cpuPercent *float64
}

// subtree is root and every process under it at a listing taken within
// reuseWithin of now, each with its CPU%, and when that listing was taken. A
// root that is not running returns no processes.
func (s *Sampler) subtree(ctx context.Context, root int) ([]observed, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listed == nil || time.Since(s.listedAt) >= reuseWithin {
		listed, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{})
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("list processes: %w", err)
		}
		s.listed, s.listedAt = listed, time.Now().UTC()
		s.rates.forget(listed.All())
	}
	processes := s.listed.Subtree(root)
	cpu, err := s.rates.observe(processes, s.listedAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	result := make([]observed, 0, len(processes))
	for _, p := range processes {
		result = append(result, observed{Process: p, cpuPercent: cpu[Key(p)]})
	}
	return result, s.listedAt, nil
}

// cpuRates holds each process's CPU baseline across listings, by Key, so a
// rate spans at least cpuWindow however often the host is listed.
type cpuRates struct {
	baselines map[string]cpuBaseline
}

// cpuBaseline is the observation the next rate is measured from, the rate of
// the last window that completed, and the listing it was last observed at.
type cpuBaseline struct {
	sample     clickyprocess.CPUSample
	percent    *float64
	observedAt time.Time
}

func newCPURates() *cpuRates { return &cpuRates{baselines: map[string]cpuBaseline{}} }

// observe is the CPU% of each process listed at at, by Key: nil until a
// window of cpuWindow has passed since its first observation, then the rate
// of its latest complete window. A zombie's or an exiting process's is nil: ps may
// already report its CPU time as 0. A process observed again at the same listing
// keeps the CPU% it had.
func (r *cpuRates) observe(processes []clickyprocess.Process, at time.Time) (map[string]*float64, error) {
	cpu := make(map[string]*float64, len(processes))
	var errs []error
	for _, p := range processes {
		key, current := Key(p), clickyprocess.CPUSample{Process: p, At: at}
		baseline, ok := r.baselines[key]
		switch {
		case ok && baseline.observedAt.Equal(at):
		case !ok || p.Status == "zombie" || p.Status == "exiting" || !samePID(baseline.sample.Process, p):
			baseline = cpuBaseline{sample: current}
		case at.Sub(baseline.sample.At) >= cpuWindow:
			percent, err := clickyprocess.IntervalCPUPercent(&baseline.sample, current)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			baseline = cpuBaseline{sample: current, percent: percent}
		}
		baseline.observedAt = at
		r.baselines[key], cpu[key] = baseline, baseline.percent
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("rate process cpu: %w", errors.Join(errs...))
	}
	return cpu, nil
}

// forget drops the baselines of processes no longer listed.
func (r *cpuRates) forget(listed []clickyprocess.Process) {
	alive := make(map[string]bool, len(listed))
	for _, p := range listed {
		alive[Key(p)] = true
	}
	for key := range r.baselines {
		if !alive[key] {
			delete(r.baselines, key)
		}
	}
}

// samePID reports whether two observations sharing a Key are one process,
// not a pid reused within the second its first holder started.
func samePID(previous, current clickyprocess.Process) bool {
	return previous.StartedAt == nil || current.StartedAt == nil || previous.StartedAt.Equal(*current.StartedAt)
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
