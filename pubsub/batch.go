package pubsub

import (
	"context"
	"errors"
	"time"
)

const lateNackTimeout = 5 * time.Second

// BatchOptions configures NewBatchSubscription.
type BatchOptions struct {
	// Fetch retrieves the next batch, blocking (long-polling) as long as it
	// likes. An empty batch is not an error: Fetch is simply called again.
	Fetch func(ctx context.Context) ([]*Message, error)
	// Close releases backend resources; called once by Shutdown.
	Close func(ctx context.Context) error
}

type batchSubscription struct {
	opts   BatchOptions
	sem    chan struct{}
	buffer []*Message
	closed bool
	life   context.Context
	stop   context.CancelFunc
}

// NewBatchSubscription adapts a batch-oriented backend (SQS, Pub/Sub pull) to
// Subscription: it buffers fetched batches, serves them one message at a time
// and, on Shutdown, nacks messages that were fetched but never delivered.
func NewBatchSubscription(o BatchOptions) Subscription {
	if o.Fetch == nil || o.Close == nil {
		panic("pubsub: NewBatchSubscription requires Fetch and Close")
	}
	life, stop := context.WithCancel(context.Background())
	return &batchSubscription{opts: o, sem: make(chan struct{}, 1), life: life, stop: stop}
}

func (s *batchSubscription) Receive(ctx context.Context) (*Message, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.sem }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.life, cancel)()

	for len(s.buffer) == 0 {
		batch, err := s.opts.Fetch(ctx)
		if s.life.Err() != nil {
			return nil, errors.Join(ErrSubscriptionClosed, nackLate(ctx, batch))
		}
		if err != nil {
			return nil, err
		}
		s.buffer = batch
	}
	msg := s.buffer[0]
	s.buffer = s.buffer[1:]
	return msg, nil
}

// acquire takes the single-receiver lock, failing when ctx ends or the
// subscription is shut down.
func (s *batchSubscription) acquire(ctx context.Context) error {
	if s.life.Err() != nil {
		return ErrSubscriptionClosed
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.life.Done():
		return ErrSubscriptionClosed
	}
}

func (s *batchSubscription) Shutdown(ctx context.Context) error {
	s.stop()
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()
	if s.closed {
		return nil
	}
	s.closed = true

	err := nackAll(ctx, s.buffer)
	s.buffer = nil
	return errors.Join(err, s.opts.Close(ctx))
}

// nackLate settles a batch fetched after shutdown began; ctx is already
// cancelled by then, so nack on a detached, bounded context.
func nackLate(ctx context.Context, batch []*Message) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lateNackTimeout)
	defer cancel()
	return nackAll(ctx, batch)
}

// nackAll returns every message to the broker. Backends without nack support
// (ErrNackUnsupported) are skipped: their messages redeliver on the broker's own timeout.
func nackAll(ctx context.Context, messages []*Message) error {
	var errs []error
	for _, msg := range messages {
		if err := msg.Nack(ctx); err != nil && !errors.Is(err, ErrNackUnsupported) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
