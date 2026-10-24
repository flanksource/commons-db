package process

import (
	"context"
	"fmt"
	"slices"

	clickyprocess "github.com/flanksource/clicky/process"
)

// facet is a reading of a process's open descriptors: which of them it
// holds, and the sample fields its count and list are stored in.
type facet struct {
	name  string
	holds func(clickyprocess.Descriptor) bool
	count func(*Sample) **int
	list  func(*Sample) *[]clickyprocess.Descriptor
}

var facets = []facet{
	{
		name: FacetOpenFiles, holds: func(d clickyprocess.Descriptor) bool { return !d.IsSocket() },
		count: func(s *Sample) **int { return &s.OpenFiles }, list: func(s *Sample) *[]clickyprocess.Descriptor { return &s.OpenFileList },
	},
	{
		name: FacetSockets, holds: clickyprocess.Descriptor.IsSocket,
		count: func(s *Sample) **int { return &s.Sockets }, list: func(s *Sample) *[]clickyprocess.Descriptor { return &s.SocketList },
	},
	{
		name: FacetPorts, holds: clickyprocess.Descriptor.IsListening,
		count: func(s *Sample) **int { return &s.Ports }, list: func(s *Sample) *[]clickyprocess.Descriptor { return &s.PortList },
	},
	{
		name: FacetConnections, holds: clickyprocess.Descriptor.IsConnected,
		count: func(s *Sample) **int { return &s.Connections }, list: func(s *Sample) *[]clickyprocess.Descriptor { return &s.ConnectionList },
	},
}

// read reads the disk IO of every process, and, when a facet is armed, the
// descriptors of every one of them with one lsof call; a process that
// exited before it was read has neither, and one the caller may not read has
// its IOError.
func (r *samplesRun) read(ctx context.Context, processes []observed) (map[int]clickyprocess.Process, map[int][]clickyprocess.Descriptor, error) {
	pids := make([]int, 0, len(processes))
	listed := make([]clickyprocess.Process, 0, len(processes))
	for _, p := range processes {
		pids, listed = append(pids, p.PID), append(listed, p.Process)
	}
	snapshot := clickyprocess.NewSnapshot(listed)
	if err := snapshot.PopulateIO(ctx, pids); err != nil {
		return nil, nil, err
	}
	io := make(map[int]clickyprocess.Process, len(pids))
	for _, p := range snapshot.All() {
		io[p.PID] = p
	}
	if len(r.params.Facets) == 0 {
		return io, nil, nil
	}
	descriptors, err := clickyprocess.Descriptors(ctx, pids...)
	if err != nil {
		return nil, nil, fmt.Errorf("read facets %v: %w", r.params.Facets, err)
	}
	return io, descriptors, nil
}

// facets sets each armed facet's count on sample, and its list when it
// differs from the one last stored for the process, or when full.
func (r *samplesRun) facets(sample *Sample, held []clickyprocess.Descriptor, full bool) {
	stored := r.stored[sample.Key]
	if stored == nil {
		stored = map[string][]clickyprocess.Descriptor{}
		r.stored[sample.Key] = stored
	}
	for _, f := range facets {
		if !r.params.armed(f.name) {
			continue
		}
		var list []clickyprocess.Descriptor
		for _, d := range held {
			if f.holds(d) {
				list = append(list, d)
			}
		}
		count := len(list)
		*f.count(sample) = &count
		previous, ok := stored[f.name]
		if full || !ok || !slices.Equal(previous, list) {
			*f.list(sample), stored[f.name] = list, list
		}
	}
}

// forget drops the stored lists of processes no longer in the tree.
func (r *samplesRun) forget(processes []observed) {
	live := make(map[string]bool, len(processes))
	for _, p := range processes {
		live[Key(p.Process)] = true
	}
	for key := range r.stored {
		if !live[key] {
			delete(r.stored, key)
		}
	}
}
