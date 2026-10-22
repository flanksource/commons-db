package sqlserver

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func validate(request Request) error {
	if err := planner.ValidateArtifact(request.Artifact); err != nil {
		return err
	}
	if request.Hash != request.Artifact.Hash {
		return fmt.Errorf("reviewed hash does not match migration artifact")
	}
	if request.LockTimeout < 0 || request.LockTimeout > math.MaxInt32*time.Millisecond {
		return fmt.Errorf("migration lock timeout must be between zero and %s", math.MaxInt32*time.Millisecond)
	}
	required := map[string]bool{}
	for _, change := range request.Artifact.Plan.Changes {
		if change.Destructive {
			required[change.ID] = true
		}
	}
	for _, id := range request.Acknowledgements {
		if !required[id] {
			return fmt.Errorf("unknown or duplicate destructive acknowledgement %q", id)
		}
		delete(required, id)
	}
	if len(required) > 0 {
		return fmt.Errorf("migration requires acknowledgements for %d destructive changes", len(required))
	}
	return nil
}

func lock(ctx context.Context, tx *sql.Tx, timeout time.Duration) error {
	var status int
	if err := tx.QueryRowContext(ctx, `DECLARE @status int;
		EXEC @status = sys.sp_getapplock @Resource=N'commons-db/migrate/schema',
		@LockMode='Exclusive', @LockOwner='Transaction', @LockTimeout=@p1;
		SELECT @status`, timeout.Milliseconds()).Scan(&status); err != nil {
		return fmt.Errorf("lock migration transaction: %w", err)
	}
	if status < 0 {
		return fmt.Errorf("lock migration transaction: sp_getapplock returned %d", status)
	}
	return nil
}
