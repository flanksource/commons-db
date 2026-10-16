package stacks

import (
	"context"
	"errors"
	"fmt"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
)

// Checkpoint dumps the stacks its params ask for (CheckpointParams), the
// root's over the first available transport without any. A dump that fails
// is one record with its error; params that cannot be dumped, as a process
// outside the tree or a transport that cannot dump it, are
// tracing.ErrCheckpointParams.
func (s stacks) Checkpoint(ctx dbcontext.Context, params Params, records tracing.Emitter[Stack]) error {
	var request CheckpointParams
	if _, err := tracing.DecodeCheckpointParams(ctx, &request); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
	}
	head := Stack{CheckpointID: newCheckpointID(), Reason: request.Reason}
	dump, err := s.take(ctx, params.root(), request, &head)
	if err != nil {
		return err
	}
	for _, record := range dump {
		if err := records.Emit(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

// take is the records of the dump request asks for, each a copy of head
// with the process and transport it was taken of.
func (s stacks) take(ctx context.Context, root int, request CheckpointParams, head *Stack) ([]Stack, error) {
	if request.Dump != nil {
		head.Key, head.PID = request.Key, request.PID
		if head.PID == 0 {
			pid, err := process.ParseKey(request.Key)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
			}
			head.PID = pid
		}
		return Records(*head, *request.Dump), nil
	}
	target, err := Resolve(ctx, root, request.Key, request.PID)
	if err != nil {
		return nil, err
	}
	head.Key, head.PID = target.Key, target.PID
	dumper, err := s.dumpers.pick(target.PID, request.Transport)
	switch {
	case errors.Is(err, errNamed):
		return nil, fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
	case err != nil:
		return []Stack{failed(*head, err)}, nil
	}
	head.Transport = dumper.Transport()
	dump, err := dumper.Dump(ctx, target.PID)
	if err != nil {
		return []Stack{failed(*head, err)}, nil
	}
	return Records(*head, dump), nil
}

func failed(head Stack, err error) Stack {
	head.TakenAt, head.Error = time.Now().UTC(), err.Error()
	return head
}

// Target is a live process of a tree a checkpoint reads: its key, as the
// process_tree kind stores it, and its pid.
type Target struct {
	Key string
	PID int
}

// Resolve is the live process of root's tree that key or pid names, or root
// when neither does. A key or pid outside the tree is
// tracing.ErrCheckpointParams; a root that is not running is an error.
func Resolve(ctx context.Context, root int, key string, pid int) (Target, error) {
	listed, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{})
	if err != nil {
		return Target{}, fmt.Errorf("list processes: %w", err)
	}
	tree := listed.Subtree(root)
	if len(tree) == 0 {
		return Target{}, fmt.Errorf("process %d is not running", root)
	}
	for _, p := range tree {
		switch {
		case key != "" && process.Key(p) == key,
			key == "" && pid != 0 && p.PID == pid,
			key == "" && pid == 0 && p.PID == root:
			return Target{Key: process.Key(p), PID: p.PID}, nil
		}
	}
	if key != "" {
		return Target{}, fmt.Errorf("%w: no live process %q in the tree of %d", tracing.ErrCheckpointParams, key, root)
	}
	return Target{}, fmt.Errorf("%w: no live process with pid %d in the tree of %d", tracing.ErrCheckpointParams, pid, root)
}
