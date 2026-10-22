// Package sqlserver executes immutable sqlglot artifacts against SQL Server.
package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/inspect/sql/snapshot"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

type Request struct {
	Artifact         migrate.Artifact
	Hash             string
	Acknowledgements []string
	LockTimeout      time.Duration
}

type Outcome string

const (
	Applied        Outcome = "applied"
	AlreadyApplied Outcome = "already-applied"
	Failed         Outcome = "failed"
	CommitUnknown  Outcome = "commit-unknown"
)

type Result struct {
	Hash    string  `json:"hash"`
	Outcome Outcome `json:"outcome"`
	Steps   int     `json:"steps"`
}

type inspectFunc func(context.Context, *sql.Tx, snapshot.Options) (snapshot.Result, error)

func Apply(ctx context.Context, db *sql.DB, request Request) (Result, error) {
	return apply(ctx, db, request, func(ctx context.Context, tx *sql.Tx, options snapshot.Options) (snapshot.Result, error) {
		return snapshot.Inspect(ctx, tx, options)
	})
}

func apply(ctx context.Context, db *sql.DB, request Request, inspect inspectFunc) (result Result, returnErr error) {
	result = Result{Hash: request.Hash, Outcome: Failed}
	if ctx == nil || db == nil {
		return result, fmt.Errorf("SQL Server artifact execution requires a context and database")
	}
	if err := validate(request); err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			returnErr = errors.Join(returnErr, fmt.Errorf("rollback migration transaction: %w", err))
			if result.Outcome == AlreadyApplied {
				result.Outcome = Failed
			}
		}
	}()
	if err := lock(ctx, tx, request.LockTimeout); err != nil {
		return result, err
	}
	state, err := inspectState(ctx, tx, request.Artifact, inspect)
	if err != nil {
		return result, err
	}
	if state == migrate.ArtifactApplied {
		result.Outcome = AlreadyApplied
		return result, nil
	}
	for _, step := range request.Artifact.Plan.Steps {
		if _, err := tx.ExecContext(ctx, executeBatch, step.SQL); err != nil {
			return result, fmt.Errorf("migration step %s: %w", step.Change.ID, err)
		}
		result.Steps++
	}
	if state, err := inspectState(ctx, tx, request.Artifact, inspect); err != nil || state != migrate.ArtifactApplied {
		return result, fmt.Errorf("migration postcondition failed (state %q): %w", state, errors.Join(err, errPostcondition))
	}
	if err := tx.Commit(); err != nil {
		result.Outcome = CommitUnknown
		return result, fmt.Errorf("migration commit outcome is unknown: %w", err)
	}
	result.Outcome = Applied
	return result, nil
}

var errPostcondition = errors.New("database does not match the complete desired snapshot")

// Nested batches keep module DDL first in its batch and contain SET options.
const executeBatch = `EXEC sys.sp_executesql N'SET XACT_ABORT ON; SET ANSI_NULLS ON; SET ANSI_PADDING ON; SET ANSI_WARNINGS ON; SET ARITHABORT ON; SET CONCAT_NULL_YIELDS_NULL ON; SET QUOTED_IDENTIFIER ON; SET NUMERIC_ROUNDABORT OFF; EXEC sys.sp_executesql @ddl;', N'@ddl nvarchar(max)', @ddl=@p1`

func inspectState(ctx context.Context, tx *sql.Tx, artifact migrate.Artifact, inspect inspectFunc) (migrate.ArtifactState, error) {
	actual, err := inspect(ctx, tx, snapshot.Options{Schemas: artifact.Schemas})
	if err != nil {
		return "", fmt.Errorf("inspect migration transaction: %w", err)
	}
	return planner.CheckArtifact(artifact, actual.Schema)
}
