package kv

import (
	"context"
	"fmt"

	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/commons-db/recordstore"
)

func (b *Backend) Delete(ctx context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	if _, err := b.readMeta(ctx, stream); err != nil {
		return err
	}
	chunks, err := b.store.ZRangeByScore(ctx, b.key(stream, "index"), cache.NegInf, cache.PosInf)
	if err != nil {
		return fmt.Errorf("stream %q: list chunks for deletion: %w", stream, err)
	}
	for _, chunk := range chunks {
		if err := b.store.Del(ctx, b.key(stream, "chunk/"+chunk)); err != nil {
			return fmt.Errorf("stream %q: delete chunk %s: %w", stream, chunk, err)
		}
	}
	rows, err := b.store.ZRangeByScore(ctx, b.key(stream, "seqs"), cache.NegInf, cache.PosInf)
	if err != nil {
		return fmt.Errorf("stream %q: list rows for deletion: %w", stream, err)
	}
	for _, member := range rows {
		_, key, err := parseRowMember(stream, member)
		if err != nil {
			return err
		}
		if err := b.store.Del(ctx, rowLayout{b}.rowKey(stream, key)); err != nil {
			return fmt.Errorf("stream %q: delete row %s: %w", stream, key, err)
		}
	}
	for _, set := range []string{"index", "seqs", "appended", "meta"} {
		if err := b.store.Del(ctx, b.key(stream, set)); err != nil {
			return fmt.Errorf("stream %q: delete %s: %w", stream, set, err)
		}
	}
	return nil
}
