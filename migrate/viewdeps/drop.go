package viewdeps

import (
	"context"
	"fmt"
)

// DropOptions configures Sweep.
type DropOptions struct {
	// Tables whose dependent views block the pending DDL. Empty is a no-op.
	Tables []Table
	// Query reads the dependency graph. Required.
	Query Querier
	// Exec runs each DROP and each restore statement. Required.
	Exec Exec
	// Owned reports whether the caller recreates this view itself. Owned views
	// are dropped and left to the caller — restoring them from the captured
	// definition would resurrect a stale one. Unowned views are captured before
	// the drop and rebuilt by the returned restore func. Nil owns nothing.
	Owned func(View) bool
	// Logf, when set, receives one line per dropped view.
	Logf func(format string, args ...any)
}

// Sweep drops every view that transitively depends on opts.Tables and returns
// the views it dropped plus a func that restores the unowned ones.
//
// Call the restore func after the schema change, and after whatever recreates
// the owned views — an unowned view may be built on top of one of them.
// Restoring is not atomic with the DDL; a failure returns a *RestoreError
// carrying the DDL so the view can be replayed by hand.
func Sweep(ctx context.Context, opts DropOptions) ([]View, func(context.Context) error, error) {
	if opts.Query == nil {
		return nil, nil, fmt.Errorf("viewdeps: DropOptions.Query is nil")
	}
	if opts.Exec == nil {
		return nil, nil, fmt.Errorf("viewdeps: DropOptions.Exec is nil")
	}
	dependents, err := Dependents(ctx, opts.Query, opts.Tables)
	if err != nil {
		return nil, nil, err
	}
	if len(dependents) == 0 {
		return nil, noRestore, nil
	}

	_, unowned := partition(dependents, opts.Owned)
	captured, err := Capture(ctx, opts.Query, unowned)
	if err != nil {
		return nil, nil, err
	}

	// Drop in reverse dependency order so a view is gone before the view it
	// reads, keeping each CASCADE to the object actually named.
	for i := len(dependents) - 1; i >= 0; i-- {
		v := dependents[i]
		if err := opts.Exec(ctx, v.DropStatement()); err != nil {
			return nil, nil, fmt.Errorf("drop dependent view %s: %w", v.Qualified(), err)
		}
		if opts.Logf != nil {
			opts.Logf("dropped dependent view %s", v.Qualified())
		}
	}
	return dependents, func(ctx context.Context) error {
		return Restore(ctx, opts.Exec, captured)
	}, nil
}

func noRestore(context.Context) error { return nil }

// partition splits views by ownership, preserving order within each group.
func partition(views []View, owned func(View) bool) (ownedViews, unowned []View) {
	for _, v := range views {
		if owned != nil && owned(v) {
			ownedViews = append(ownedViews, v)
			continue
		}
		unowned = append(unowned, v)
	}
	return ownedViews, unowned
}
