package pubsub

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrNackUnsupported is returned by Message.Nack when the backend cannot
	// hand a message back for redelivery (Kafka, for example).
	ErrNackUnsupported = errors.New("pubsub: backend does not support nack")

	// ErrAlreadySettled is returned when a message is acked or nacked twice.
	ErrAlreadySettled = errors.New("pubsub: message already acked or nacked")

	// ErrSubscriptionClosed is returned by Receive once the subscription is shut down.
	ErrSubscriptionClosed = errors.New("pubsub: subscription is shut down")
)

// Message is a received message. Exactly one of Ack or Nack must be called once
// the message has been handled; unsettled messages are redelivered by the
// backend after its own timeout.
type Message struct {
	// ID is an opaque identifier suitable for logging.
	ID       string
	Body     []byte
	Metadata map[string]string

	ack, nack func(context.Context) error

	mu      sync.Mutex
	settled bool
}

// MessageOptions builds a Message. Ack is required; a nil Nack makes
// Message.Nack return ErrNackUnsupported.
type MessageOptions struct {
	ID       string
	Body     []byte
	Metadata map[string]string
	Ack      func(context.Context) error
	Nack     func(context.Context) error
}

// NewMessage builds a Message; it is meant for backend implementations.
func NewMessage(o MessageOptions) *Message {
	if o.Ack == nil {
		panic("pubsub: NewMessage requires an Ack function")
	}
	return &Message{ID: o.ID, Body: o.Body, Metadata: o.Metadata, ack: o.Ack, nack: o.Nack}
}

// Ack marks the message as processed so the backend does not redeliver it.
func (m *Message) Ack(ctx context.Context) error { return m.settle(ctx, "ack", m.ack) }

// Nack hands the message back for immediate redelivery.
func (m *Message) Nack(ctx context.Context) error { return m.settle(ctx, "nack", m.nack) }

func (m *Message) settle(ctx context.Context, verb string, fn func(context.Context) error) error {
	if fn == nil {
		return ErrNackUnsupported
	}
	m.mu.Lock()
	if m.settled {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s of message %s", ErrAlreadySettled, verb, m.ID)
	}
	m.settled = true
	m.mu.Unlock()

	if err := fn(ctx); err != nil {
		m.mu.Lock()
		m.settled = false
		m.mu.Unlock()
		return fmt.Errorf("pubsub: %s message %s: %w", verb, m.ID, err)
	}
	return nil
}

// Subscription delivers messages from one queue, one at a time.
type Subscription interface {
	// Receive blocks until a message is available, ctx is done, or the
	// subscription is shut down (ErrSubscriptionClosed).
	Receive(ctx context.Context) (*Message, error)
	// Shutdown stops the subscription and releases its resources.
	Shutdown(ctx context.Context) error
}
