// Package kafka subscribes to Kafka topics through the gocloud.dev kafkapubsub driver.
package kafka

import (
	"context"
	"fmt"

	gcpubsub "gocloud.dev/pubsub"
	"gocloud.dev/pubsub/kafkapubsub"

	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
)

// Install registers the Kafka backend with pubsub.Subscribe.
func Install() { pubsub.Register(pubsub.KindKafka, Open) }

// Open subscribes to the topic described by c.Kafka. Kafka cannot nack, so
// Message.Nack returns pubsub.ErrNackUnsupported.
func Open(_ commonsContext.Context, c pubsub.QueueConfig) (pubsub.Subscription, error) {
	if c.Kafka == nil {
		return nil, fmt.Errorf("pubsub/kafka: QueueConfig has no kafka section")
	}
	sub, err := kafkapubsub.OpenSubscription(c.Kafka.Brokers, nil, c.Kafka.Group, []string{c.Kafka.Topic}, nil)
	if err != nil {
		return nil, err
	}
	return pubsub.NewBatchSubscription(pubsub.BatchOptions{
		Fetch: func(ctx context.Context) ([]*pubsub.Message, error) { return receive(ctx, sub) },
		Close: sub.Shutdown,
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
