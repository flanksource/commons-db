package workspace

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// Phase is the pipeline step a Check comes from.
type Phase string

const (
	PhaseCommit Phase = "commit"
	PhasePush   Phase = "push"
)

// Check is what a gate inspects. For a commit, Paths is the staged set and
// Changes the index against From (HEAD). For a push, From..To is the outgoing
// range, Paths every path any commit in it touched — including ones a later
// commit removed, which still travel in history — and Changes the net diff.
type Check struct {
	Phase    Phase
	From, To string
	Paths    []string
	Changes  []Change
}

// Gate inspects a Check and may block it; it never mutates the workspace.
type Gate interface {
	Name() string
	Check(ctx context.Context, ws *Workspace, c Check) error
}

// Hook runs before a commit's gates and may rewrite files (formatters, tidy).
// The pipeline restages the commit's paths after every hook has run.
type Hook interface {
	Name() string
	Run(ctx context.Context, ws *Workspace, paths []string) error
}

// GateError is a gate's refusal, naming what it blocked.
type GateError struct {
	Gate     string
	Phase    Phase
	Findings []string
}

func (e *GateError) Error() string {
	return fmt.Sprintf("workspace: %s gate blocked %s: %s", e.Gate, e.Phase, strings.Join(e.Findings, ", "))
}

// ExcludesGateName is the one gate that always runs: content cloned from the
// source's gitignored files never leaves the workspace.
const ExcludesGateName = "excludes"

type excludesGate struct{}

func (excludesGate) Name() string { return ExcludesGateName }

func (excludesGate) Check(_ context.Context, ws *Workspace, c Check) error {
	var findings []string
	for _, path := range c.Paths {
		if ws.IsExcluded(path) {
			findings = append(findings, path)
		}
	}
	if len(findings) > 0 {
		return &GateError{Gate: ExcludesGateName, Phase: c.Phase, Findings: findings}
	}
	return nil
}

// runGates runs the excludes gate, then the configured gates in order, and
// stops at the first refusal.
func (w *Workspace) runGates(ctx context.Context, c Check, gates []Gate) error {
	for _, gate := range append([]Gate{excludesGate{}}, gates...) {
		if err := gate.Check(ctx, w, c); err != nil {
			var gateErr *GateError
			if errors.As(err, &gateErr) {
				return err
			}
			return fmt.Errorf("workspace: %s gate: %w", gate.Name(), err)
		}
	}
	return nil
}
