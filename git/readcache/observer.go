package readcache

import "context"

type Event struct {
	Metadata
	ID          string
	Key         string
	Disposition string
	Done        bool
	Err         error
}

type Observer func(context.Context, Event)
type observerKey struct{}

type Metadata struct{ Operation, Repository, Ref string }
type metadataKey struct{}

func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, metadata)
}

func WithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

func report(ctx context.Context, event Event) {
	if observer, ok := ctx.Value(observerKey{}).(Observer); ok && observer != nil {
		observer(ctx, event)
	}
}
