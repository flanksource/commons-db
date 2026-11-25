// Package nats subscribes to NATS subjects through the gocloud.dev natspubsub driver.
package nats

import (
	"context"
	"fmt"

	natsgo "github.com/nats-io/nats.go"
	gcpubsub "gocloud.dev/pubsub"
	"gocloud.dev/pubsub/natspubsub"

	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
)

// Install registers the NATS backend with pubsub.Subscribe.
func Install() { pubsub.Register(pubsub.KindNATS, Open) }

// Open subscribes to the subject described by c.NATS. Core NATS is
// fire-and-forget, so Message.Nack returns pubsub.ErrNackUnsupported. The
// connection is closed when the subscription is shut down.
func Open(_ commonsContext.Context, c pubsub.QueueConfig) (pubsub.Subscription, error) {
	if c.NATS == nil {
		return nil, fmt.Errorf("pubsub/nats: QueueConfig has no nats section")
	}
	conn, err := natsgo.Connect(c.NATS.URL)
	if err != nil {
		return nil, err
	}
	sub, err := natspubsub.OpenSubscriptionV2(conn, c.NATS.Subject, &natspubsub.SubscriptionOptions{Queue: c.NATS.Queue})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return pubsub.NewBatchSubscription(pubsub.BatchOptions{
		Fetch: func(ctx context.Context) ([]*pubsub.Message, error) { return receive(ctx, sub) },
		Close: func(ctx context.Context) error {
			defer conn.Close()
			return sub.Shutdown(ctx)
		},
	}), nil
}

func receive(ctx context.Context, sub *gcpubsub.Subscription) ([]*pubsub.Message, error) {
	msg, err := sub.Receive(ctx)
	if err != nil {
		return nil, err
	}
	opts := pubsub.MessageOptions{
		ID: msg.LoggableID, Body: msg.Body, Metadata: msg.Metadata,
		Ack: func(context.Context) error { msg.Ack(); return nil },
	}
	if msg.Nackable() {
		opts.Nack = func(context.Context) error { msg.Nack(); return nil }
	}
	return []*pubsub.Message{pubsub.NewMessage(opts)}, nil
}
