package ndjson

import (
	"context"

	"github.com/flanksource/commons-db/recordstore"
)

func (b *Backend) Delete(_ context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil {
		return err
	}
	return b.remove(state)
}
