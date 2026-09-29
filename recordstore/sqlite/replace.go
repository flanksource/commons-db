// Replacing stored rows: an append to a kind that replaces them removes the
// rows its keys name before storing the appended ones after the high seq.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/db/sqlitetable"
)

// replaceStoredRows removes, in tx, the rows of stream whose key is one of
// keys, and returns how many it removed.
func replaceStoredRows(ctx context.Context, tx *sql.Tx, table kindTable, stream string, keys []string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	streamID, err := table.Physical(streamColumn)
	if err != nil {
		return 0, err
	}
	statement, err := tx.PrepareContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ? AND %s = ?`,
		sqlitetable.QuoteIdentifier(table.Name), streamID, table.key))
	if err != nil {
		return 0, fmt.Errorf("stream %q: prepare replace: %w", stream, err)
	}
	defer func() { _ = statement.Close() }()
	var replaced int64
	for _, key := range keys {
		result, err := statement.ExecContext(ctx, stream, key)
		if err != nil {
			return 0, fmt.Errorf("stream %q: replace key %q: %w", stream, key, err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("stream %q: count rows replaced under key %q: %w", stream, key, err)
		}
		replaced += removed
	}
	return replaced, nil
}
