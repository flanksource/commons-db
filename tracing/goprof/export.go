package goprof

import (
	"fmt"
	"io"
	"iter"

	"github.com/google/pprof/profile"

	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/stacks"
)

// PProf is the format a window downloads in as one pprof profile, which
// go tool pprof reads.
var PProf = tracing.ExportFormat{Name: "pprof", Ext: "pb.gz", MIME: "application/octet-stream"}

func (profiler) Formats() []tracing.ExportFormat { return []tracing.ExportFormat{PProf} }

// Summarize is a window as its checkpoints and each sample type's total.
func (profiler) Summarize(rows iter.Seq[Sample]) (map[string]any, error) {
	checkpoints := map[string]bool{}
	totals := map[string]int64{}
	for record := range rows {
		checkpoints[record.CheckpointID] = true
		totals[record.SampleType+"/"+record.Unit] += record.Value
	}
	return map[string]any{"checkpoints": len(checkpoints), "totals": totals}, nil
}

// Export rebuilds the window's records into one pprof profile: its sample
// types in the order they first appear, each stack's values summed across
// the window's checkpoints. A window that holds more than one profile, such
// as cpu and heap, is refused.
func (profiler) Export(format string, rows iter.Seq[Sample], w io.Writer) error {
	if format != PProf.Name {
		return fmt.Errorf("%w %q for trace kind %s", tracing.ErrUnknownFormat, format, Kind)
	}
	built, err := rebuild(rows)
	if err != nil {
		return err
	}
	if err := built.CheckValid(); err != nil {
		return fmt.Errorf("rebuild pprof profile: %w", err)
	}
	return built.Write(w)
}

// builder holds a profile being rebuilt and the ids it gave its sample
// types, locations, functions and stacks.
type builder struct {
	profile   *profile.Profile
	types     map[profile.ValueType]int
	locations map[stacks.Frame]*profile.Location
	functions map[[2]string]*profile.Function
	samples   map[string]*profile.Sample
}

func rebuild(rows iter.Seq[Sample]) (*profile.Profile, error) {
	b := builder{
		profile: &profile.Profile{}, types: map[profile.ValueType]int{},
		locations: map[stacks.Frame]*profile.Location{}, functions: map[[2]string]*profile.Function{}, samples: map[string]*profile.Sample{},
	}
	kind := ""
	var records []Sample
	for record := range rows {
		if kind != "" && record.Profile != kind {
			return nil, fmt.Errorf("the window holds %s and %s profiles: export one profile's checkpoints", kind, record.Profile)
		}
		kind = record.Profile
		if record.TakenAt.UnixNano() > 0 && b.profile.TimeNanos == 0 {
			b.profile.TimeNanos = record.TakenAt.UnixNano()
		}
		b.sampleType(record)
		records = append(records, record)
	}
	for _, record := range records {
		b.sample(record).Value[b.types[profile.ValueType{Type: record.SampleType, Unit: record.Unit}]] += record.Value
	}
	return b.profile, nil
}

func (b *builder) sampleType(record Sample) {
	key := profile.ValueType{Type: record.SampleType, Unit: record.Unit}
	if _, ok := b.types[key]; !ok {
		b.types[key] = len(b.profile.SampleType)
		b.profile.SampleType = append(b.profile.SampleType, &profile.ValueType{Type: record.SampleType, Unit: record.Unit})
	}
}

// sample is the profile's sample of record's stack, made with a value per
// sample type the first time the stack is seen.
func (b *builder) sample(record Sample) *profile.Sample {
	if sample, ok := b.samples[record.StackHash]; ok {
		return sample
	}
	sample := &profile.Sample{Value: make([]int64, len(b.profile.SampleType))}
	for _, frame := range record.Frames {
		sample.Location = append(sample.Location, b.location(frame))
	}
	b.samples[record.StackHash] = sample
	b.profile.Sample = append(b.profile.Sample, sample)
	return sample
}

func (b *builder) location(frame stacks.Frame) *profile.Location {
	if location, ok := b.locations[frame]; ok {
		return location
	}
	key := [2]string{frame.Func, frame.File}
	function, ok := b.functions[key]
	if !ok {
		function = &profile.Function{ID: uint64(len(b.profile.Function) + 1), Name: frame.Func, SystemName: frame.Func, Filename: frame.File}
		b.functions[key] = function
		b.profile.Function = append(b.profile.Function, function)
	}
	location := &profile.Location{ID: uint64(len(b.profile.Location) + 1), Line: []profile.Line{{Function: function, Line: int64(frame.Line)}}}
	b.locations[frame] = location
	b.profile.Location = append(b.profile.Location, location)
	return location
}
