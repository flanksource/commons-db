package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/recordstore"
)

func (b *Backend) Delete(ctx context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	return b.database.Write(func(writer *sql.DB) error {
		meta, err := loadMeta(ctx, writer, stream)
		if err != nil {
			return err
		}
		var table string
		if err := writer.QueryRowContext(ctx, `SELECT table_name FROM record_kinds WHERE kind = ?`, meta.Kind).Scan(&table); err != nil {
			return fmt.Errorf("stream %q: read table for kind %q: %w", stream, meta.Kind, err)
		}
		return b.removeStream(ctx, writer, stream, table)
	})
}
