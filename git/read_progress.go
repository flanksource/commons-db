package git

import "context"

// ReadProgress reports measured object IO. Total is zero for an open-ended tree reader.
type ReadProgress struct {
	ID, Repository, Ref   string
	Current, Total, Bytes int64
	BytesPerSecond        float64
	Done                  bool
	Err                   error
}

type ReadObserver func(context.Context, ReadProgress)
type readObserverKey struct{}
type readIdentityKey struct{}
type readIdentity struct{ repository, ref string }

func WithReadObserver(ctx context.Context, observer ReadObserver) context.Context {
	return context.WithValue(ctx, readObserverKey{}, observer)
}

func withReadIdentity(ctx context.Context, repository, ref string) context.Context {
	return context.WithValue(ctx, readIdentityKey{}, readIdentity{repository, ref})
}
