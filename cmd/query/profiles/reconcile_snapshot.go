package profiles

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

type ReconcileSnapshotDescriptor struct {
	ID            string               `json:"id"`
	Connection    string               `json:"connection"`
	ConnectionID  string               `json:"connection_id"`
	Profile       string               `json:"profile"`
	Surface       string               `json:"surface"`
	URL           string               `json:"url"`
	Columns       []query.ColumnDef    `json:"columns"`
	RowCount      int                  `json:"row_count"`
	Stats         query.ReconcileStats `json:"stats"`
	Source        string               `json:"source"`
	Dest          string               `json:"dest"`
	SourceLimited bool                 `json:"source_truncated,omitempty"`
	DestLimited   bool                 `json:"dest_truncated,omitempty"`
	IdleAge       time.Duration        `json:"idle_age"`
	ExpiresAt     time.Time            `json:"expires_at"`
}

func (d ReconcileSnapshotDescriptor) ColumnNames() []string {
	names := make([]string, 0, len(d.Columns))
	for _, column := range d.Columns {
		if !column.Hidden {
			names = append(names, column.Name)
		}
	}
	return names
}

type ReconcileMaterializeOptions struct {
	SnapshotID string   `flag:"snapshot" help:"Reconciliation snapshot id"`
	Profile    string   `flag:"profile" help:"Snapshot profile to transform or project"`
	CEL        string   `flag:"cel" help:"CEL expression returning the rows to materialize"`
	Columns    []string `flag:"column" help:"Output column to include, in order (repeatable)"`
}

func (ReconcileMaterializeOptions) ClickyActionFlags() {}

type SnapshotService interface {
	Create(context.Context, *query.ReconcileResult, time.Duration) (ReconcileSnapshotDescriptor, error)
	Materialize(context.Context, ReconcileMaterializeOptions) (ReconcileSnapshotDescriptor, error)
}

func (s *Service) ReconcileSnapshot(ctx context.Context, name string, options ReconcileFlags) (ReconcileSnapshotDescriptor, error) {
	if s.snapshots == nil {
		return ReconcileSnapshotDescriptor{}, fmt.Errorf("reconciliation snapshots are not configured")
	}
	age := time.Duration(0)
	if strings.TrimSpace(options.SnapshotAge) != "" {
		parsed, err := time.ParseDuration(options.SnapshotAge)
		if err != nil {
			return ReconcileSnapshotDescriptor{}, fmt.Errorf("invalid --snapshot-age %q: %w", options.SnapshotAge, err)
		}
		age = parsed
	}
	fullResult := options
	fullResult.Outcome = ""
	result, err := s.Reconcile(ctx, name, fullResult)
	if err != nil {
		return ReconcileSnapshotDescriptor{}, err
	}
	return s.snapshots.Create(ctx, result, age)
}

func (s *Service) MaterializeReconcile(ctx context.Context, options ReconcileMaterializeOptions) (ReconcileSnapshotDescriptor, error) {
	if s.snapshots == nil {
		return ReconcileSnapshotDescriptor{}, fmt.Errorf("reconciliation snapshots are not configured")
	}
	return s.snapshots.Materialize(ctx, options)
}
