package sqlite

import (
	"context"
	"database/sql"

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
		return b.removeStream(ctx, writer, stream, meta.Kind)
	})
}
