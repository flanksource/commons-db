package git

import (
	"context"
	"path/filepath"
	"sync"
)

var repositoryReaders sync.Map

func readRepository(ctx context.Context, mirror string, read func() error) error {
	release, err := acquireRepositoryReader(ctx, mirror)
	if err != nil {
		return err
	}
	defer release()
	return read()
}

func acquireRepositoryReader(ctx context.Context, directory string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	value, _ := repositoryReaders.LoadOrStore(canonical, make(chan struct{}, 4))
	readers := value.(chan struct{})
	select {
	case readers <- struct{}{}:
		return func() { <-readers }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
