package pubsub_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/pubsub"
)

// recorder tracks the settlement calls made on messages it builds.
type recorder struct {
	mu    sync.Mutex
	acked []string
	nacks []string
}

func (r *recorder) message(id string, nackable bool) *pubsub.Message {
	opts := pubsub.MessageOptions{
		ID: id, Body: []byte("body-" + id),
		Ack: func(context.Context) error { r.record(&r.acked, id); return nil },
	}
	if nackable {
		opts.Nack = func(context.Context) error { r.record(&r.nacks, id); return nil }
	}
	return pubsub.NewMessage(opts)
}

func (r *recorder) record(into *[]string, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*into = append(*into, id)
}

var _ = Describe("Message", func() {
	It("rejects a second settlement and reports unsupported nack", func(ctx SpecContext) {
		r := &recorder{}
		m := r.message("a", false)

		Expect(m.Nack(ctx)).To(MatchError(pubsub.ErrNackUnsupported))
		Expect(m.Ack(ctx)).To(Succeed())
		Expect(m.Ack(ctx)).To(MatchError(pubsub.ErrAlreadySettled))
		Expect(r.acked).To(Equal([]string{"a"}))
	})

	It("lets a failed ack be retried", func(ctx SpecContext) {
		calls := 0
		m := pubsub.NewMessage(pubsub.MessageOptions{ID: "x", Ack: func(context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("network down")
			}
			return nil
		}})

		Expect(m.Ack(ctx)).To(MatchError(ContainSubstring("network down")))
		Expect(m.Ack(ctx)).To(Succeed())
	})
})

var _ = Describe("NewBatchSubscription", func() {
	It("serves a fetched batch one message at a time and refetches on empty batches", func(ctx SpecContext) {
		r := &recorder{}
		batches := [][]*pubsub.Message{nil, {r.message("1", true), r.message("2", true)}, {r.message("3", true)}}
		sub := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(context.Context) ([]*pubsub.Message, error) {
				next := batches[0]
				batches = batches[1:]
				return next, nil
			},
			Close: func(context.Context) error { return nil },
		})

		var ids []string
		for range 3 {
			m, err := sub.Receive(ctx)
			Expect(err).ToNot(HaveOccurred())
			ids = append(ids, m.ID)
		}
		Expect(ids).To(Equal([]string{"1", "2", "3"}))
	})

	It("nacks buffered messages on shutdown and ends a blocked Receive with ErrSubscriptionClosed", func(ctx SpecContext) {
		r := &recorder{}
		served := false
		closed := 0
		sub := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(ctx context.Context) ([]*pubsub.Message, error) {
				if !served {
					served = true
					return []*pubsub.Message{r.message("1", true), r.message("2", true)}, nil
				}
				<-ctx.Done()
				return nil, ctx.Err()
			},
			Close: func(context.Context) error { closed++; return nil },
		})
		first, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(first.ID).To(Equal("1"))
		second, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(second.ID).To(Equal("2"))

		result := make(chan error, 1)
		go func() { _, err := sub.Receive(context.Background()); result <- err }()
		time.Sleep(50 * time.Millisecond)

		Expect(sub.Shutdown(ctx)).To(Succeed())
		Expect(<-result).To(MatchError(pubsub.ErrSubscriptionClosed))
		Expect(closed).To(Equal(1))
		Expect(sub.Shutdown(ctx)).To(Succeed())
		Expect(closed).To(Equal(1))
		_, err = sub.Receive(ctx)
		Expect(err).To(MatchError(pubsub.ErrSubscriptionClosed))
	})

	It("nacks undelivered messages when shutting down", func(ctx SpecContext) {
		r := &recorder{}
		sub := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(context.Context) ([]*pubsub.Message, error) {
				return []*pubsub.Message{r.message("1", true), r.message("2", true), r.message("3", false)}, nil
			},
			Close: func(context.Context) error { return nil },
		})
		_, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())

		Expect(sub.Shutdown(ctx)).To(Succeed())
		Expect(r.nacks).To(Equal([]string{"2"}))
	})

	It("nacks a batch that arrives while shutting down instead of dropping it", func(ctx SpecContext) {
		r := &recorder{}
		fetching, release := make(chan struct{}), make(chan struct{})
		sub := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(context.Context) ([]*pubsub.Message, error) {
				close(fetching)
				<-release
				return []*pubsub.Message{r.message("1", true), r.message("2", false), r.message("3", true)}, nil
			},
			Close: func(context.Context) error { return nil },
		})
		received := make(chan error, 1)
		go func() { _, err := sub.Receive(context.Background()); received <- err }()
		<-fetching
		shutdown := make(chan error, 1)
		go func() { shutdown <- sub.Shutdown(context.Background()) }()
		time.Sleep(50 * time.Millisecond)
		close(release)

		Expect(<-received).To(MatchError(pubsub.ErrSubscriptionClosed))
		Expect(<-shutdown).To(Succeed())
		Expect(r.nacks).To(Equal([]string{"1", "3"}))
	})

	It("surfaces fetch errors", func(ctx SpecContext) {
		sub := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(context.Context) ([]*pubsub.Message, error) { return nil, fmt.Errorf("throttled") },
			Close: func(context.Context) error { return nil },
		})
		_, err := sub.Receive(ctx)
		Expect(err).To(MatchError("throttled"))
	})
})
