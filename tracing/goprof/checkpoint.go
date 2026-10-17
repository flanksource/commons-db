package goprof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/gops/signal"
	"github.com/google/pprof/profile"
	"github.com/google/uuid"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
	"github.com/flanksource/commons-db/tracing/stacks"
)

// Checkpoint reads the profile its params ask for (CheckpointParams) and
// emits its samples. Params it cannot read, as a process outside the tree or
// one without an address, are tracing.ErrCheckpointParams.
func (p profiler) Checkpoint(ctx dbcontext.Context, params Params, records tracing.Emitter[Sample]) error {
	var request CheckpointParams
	given, err := tracing.DecodeCheckpointParams(ctx, &request)
	if err != nil {
		return err
	}
	if !given {
		return fmt.Errorf("%w: a goprof checkpoint needs params naming the profile to read", tracing.ErrCheckpointParams)
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
	}
	head := Sample{CheckpointID: uuid.NewString(), Reason: request.Reason, Source: request.Source, Profile: request.Profile}
	data, err := p.read(ctx, params.root(), request, &head)
	if err != nil {
		return err
	}
	parsed, err := profile.ParseData(data)
	if err != nil {
		return fmt.Errorf("parse %s %s profile of %s: %w", request.Source, request.Profile, head.Key, err)
	}
	for _, sample := range Aggregate(head, parsed) {
		if err := records.Emit(ctx, sample); err != nil {
			return err
		}
	}
	return nil
}

// read is the profile request names, setting the process it was read of and
// when on head.
func (p profiler) read(ctx context.Context, root int, request CheckpointParams, head *Sample) ([]byte, error) {
	head.TakenAt = time.Now().UTC()
	if request.Source == SourceFile {
		head.Key, head.PID = request.Key, request.PID
		if head.PID == 0 {
			pid, err := process.ParseKey(request.Key)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
			}
			head.PID = pid
		}
		data, err := os.ReadFile(request.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
		}
		return data, nil
	}
	target, err := stacks.Resolve(ctx, root, request.Key, request.PID)
	if err != nil {
		return nil, err
	}
	head.Key, head.PID = target.Key, target.PID
	var addr string
	ok := p.address != nil
	if ok {
		addr, ok = p.address(target.PID)
	}
	if !ok {
		return nil, fmt.Errorf("%w: process %d has no %s address", tracing.ErrCheckpointParams, target.PID, request.Source)
	}
	if request.Source == SourceGops {
		return stacks.AskGops(ctx, addr, map[string]byte{"cpu": signal.CPUProfile, "heap": signal.HeapProfile}[request.Profile])
	}
	if request.Profile == "cpu" {
		return stacks.FetchPProf(ctx, addr, "profile", "seconds="+strconv.Itoa(request.Seconds))
	}
	return stacks.FetchPProf(ctx, addr, request.Profile, "")
}

// Aggregate is the records of parsed, each a copy of head: per sample type,
// in the profile's order, the value each stack holds, in the order the stacks
// first appear. A stack that holds nothing of a type has no record of it.
func Aggregate(head Sample, parsed *profile.Profile) []Sample {
	type stack struct {
		frames []stacks.Frame
		values []int64
	}
	var order []string
	byHash := map[string]*stack{}
	for _, sample := range parsed.Sample {
		frames := framesOf(sample.Location)
		hash := stackHash(frames)
		aggregated, ok := byHash[hash]
		if !ok {
			aggregated = &stack{frames: frames, values: make([]int64, len(parsed.SampleType))}
			byHash[hash] = aggregated
			order = append(order, hash)
		}
		for index, value := range sample.Value {
			aggregated.values[index] += value
		}
	}
	var records []Sample
	for index, sampleType := range parsed.SampleType {
		for _, hash := range order {
			if value := byHash[hash].values[index]; value != 0 {
				record := head
				record.SampleType, record.Unit, record.Value = sampleType.Type, sampleType.Unit, value
				record.Frames, record.StackHash = byHash[hash].frames, hash
				records = append(records, record)
			}
		}
	}
	return records
}

// framesOf are a sample's calls innermost first: each location's lines, its
// inlined calls first, or its address when it has none.
func framesOf(locations []*profile.Location) []stacks.Frame {
	var frames []stacks.Frame
	for _, location := range locations {
		if len(location.Line) == 0 {
			frames = append(frames, stacks.Frame{Func: fmt.Sprintf("0x%x", location.Address)})
		}
		for _, line := range location.Line {
			frame := stacks.Frame{Line: int(line.Line)}
			if line.Function != nil {
				frame.Func, frame.File = line.Function.Name, line.Function.Filename
			}
			frames = append(frames, frame)
		}
	}
	return frames
}

func stackHash(frames []stacks.Frame) string {
	hash := sha256.New()
	for _, frame := range frames {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\n", frame.Func, frame.File, frame.Line)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
