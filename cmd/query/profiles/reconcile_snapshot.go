package profiles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/clicky/entity"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// ErrSnapshotNotFound distinguishes an id that never existed from one that
// expired. The two get different HTTP statuses because the browser says
// different things: one is "come back with a real link", the other is "this
// aged out, run it again".
var ErrSnapshotNotFound = errors.New("reconciliation snapshot not found")

type ReconcileSnapshotDescriptor struct {
	ID           string               `json:"id"`
	Connection   string               `json:"connection"`
	ConnectionID string               `json:"connection_id"`
	Profile      string               `json:"profile"`
	Surface      string               `json:"surface"`
	URL          string               `json:"url"`
	Columns      []query.ColumnDef    `json:"columns"`
	RowCount     int                  `json:"row_count"`
	Stats        query.ReconcileStats `json:"stats"`
	Source       string               `json:"source"`
	Dest         string               `json:"dest"`

	// Reconcile is how this snapshot was produced — the config the run used and
	// what each side asked its backend. It is stored with the rows so a results
	// page opened days later shows the query that produced them rather than one
	// re-derived from a profile that has since changed.
	Reconcile *ReconcileSnapshotProvenance `json:"reconcile,omitempty"`

	SourceLimited bool          `json:"source_truncated,omitempty"`
	DestLimited   bool          `json:"dest_truncated,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	IdleAge       time.Duration `json:"idle_age"`
	ExpiresAt     time.Time     `json:"expires_at"`
}

// ReconcileSnapshotProvenance pairs what a run was asked to do with what it
// did. The two are separate fields rather than one flattened object because
// they answer different questions and only the first can be replayed.
type ReconcileSnapshotProvenance struct {
	Config    query.ReconcileConfig      `json:"config"`
	Execution *query.ReconcileProvenance `json:"execution,omitempty"`
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
	// Describe returns a stored snapshot's base descriptor. A materialized
	// projection stays reachable through Materialize, which is idempotent by
	// fingerprint — there is deliberately only one read path.
	Describe(context.Context, string) (ReconcileSnapshotDescriptor, error)
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

type ReconcileSnapshotFlags struct {
	SnapshotID string `flag:"snapshot" help:"Reconciliation snapshot id"`
}

func (ReconcileSnapshotFlags) ClickyActionFlags() {}

// GetReconcileSnapshot reads a stored reconciliation by id.
//
// The statuses matter more than usual here: the browser opens this URL from a
// bookmark or a shared link, and "this aged out, run it again" and "that is not
// a snapshot" are different things to tell someone. The codes match the ones
// this server already uses for an expired snapshot connection.
func (s *Service) GetReconcileSnapshot(
	ctx context.Context, profileName string, options ReconcileSnapshotFlags,
) (ReconcileSnapshotDescriptor, error) {
	if s.snapshots == nil {
		return ReconcileSnapshotDescriptor{}, fmt.Errorf("reconciliation snapshots are not configured")
	}
	id := strings.TrimSpace(options.SnapshotID)
	if id == "" {
		return ReconcileSnapshotDescriptor{}, entity.NewStatusError(
			http.StatusBadRequest, "invalid_request", "--snapshot is required")
	}
	descriptor, err := s.snapshots.Describe(ctx, id)
	switch {
	case errors.Is(err, dbcontext.ErrConnectionExpired):
		return ReconcileSnapshotDescriptor{}, entity.NewStatusErrorf(
			http.StatusGone, "snapshot_expired", "reconciliation snapshot %q has expired", id)
	case errors.Is(err, ErrSnapshotNotFound):
		return ReconcileSnapshotDescriptor{}, entity.NewStatusErrorf(
			http.StatusNotFound, "snapshot_not_found", "reconciliation snapshot %q not found", id)
	case err != nil:
		return ReconcileSnapshotDescriptor{}, err
	}
	// The {id} segment is the source profile, which is what keeps one profile's
	// route from serving another profile's snapshot.
	if profileName != "" && descriptor.Source != profileName {
		return ReconcileSnapshotDescriptor{}, entity.NewStatusErrorf(
			http.StatusNotFound, "snapshot_not_found",
			"reconciliation snapshot %q does not belong to profile %q", id, profileName)
	}
	return descriptor, nil
}
