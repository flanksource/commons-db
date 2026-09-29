// The batch ledger: every batch the file applied, by id, with its outcome,
// written in the batch's own transaction so a batch is applied exactly once.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/recordstore"
)

// ledgerStatement creates the ledger. outcome says how a batch ended, such as
// batchApplied; results is its recordstore.BatchResult.
const ledgerStatement = `CREATE TABLE record_spool_batches (
	batch_id TEXT PRIMARY KEY, producer TEXT NOT NULL, producer_seq INTEGER NOT NULL,
	outcome TEXT NOT NULL, results TEXT NOT NULL, ingested_at TEXT NOT NULL)`

// batchApplied is the outcome of a batch whose entries were applied, each one
// committed or rolled back with its error in the recorded results.
const batchApplied = "applied"

// recordBatchTx records in tx that batch was applied with result.
func recordBatchTx(ctx context.Context, tx *sql.Tx, producer recordstore.Producer, result recordstore.BatchResult, now time.Time) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("batch %q: encode results: %w", result.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_spool_batches (batch_id, producer, producer_seq, outcome, results, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?)`, result.ID, producer.Instance, producer.Seq, batchApplied, string(encoded), sqlitetable.FormatTime(now)); err != nil {
		return fmt.Errorf("batch %q: record in the ledger: %w", result.ID, err)
	}
	return nil
}

// readBatchOutcome reads batch id's recorded result, reporting false for a
// batch the ledger does not hold.
func readBatchOutcome(ctx context.Context, database queryer, id string) (recordstore.BatchResult, bool, error) {
	var encoded string
	err := database.QueryRowContext(ctx, `SELECT results FROM record_spool_batches WHERE batch_id = ?`, id).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return recordstore.BatchResult{}, false, nil
	}
	if err != nil {
		return recordstore.BatchResult{}, false, fmt.Errorf("batch %q: read the ledger: %w", id, err)
	}
	var result recordstore.BatchResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return recordstore.BatchResult{}, false, fmt.Errorf("batch %q: decode recorded results: %w", id, err)
	}
	return result, true, nil
}

// BatchOutcome reads batch id's recorded outcome.
func (b *Backend) BatchOutcome(ctx context.Context, id string) (recordstore.BatchResult, bool, error) {
	return readBatchOutcome(ctx, b.database.Reader(), id)
}
