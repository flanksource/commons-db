package pubsub_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
)

var _ = Describe("Subscribe", func() {
	It("fails without any queue configuration", func() {
		_, err := pubsub.Subscribe(commonsContext.New(), pubsub.QueueConfig{})
		Expect(err).To(MatchError(ContainSubstring("no queue configuration")))
	})

	DescribeTable("an uninstalled backend names the subpackage to install",
		func(config pubsub.QueueConfig, subpackage string) {
			_, err := pubsub.Subscribe(commonsContext.New(), config)
			Expect(err).To(MatchError(And(ContainSubstring("not installed"), ContainSubstring("commons-db/pubsub/"+subpackage+" "), ContainSubstring("Install()"))))
		},
		Entry("sqs", pubsub.QueueConfig{SQS: &pubsub.SQSConfig{}}, "sqs"),
		Entry("gcp pubsub", pubsub.QueueConfig{PubSub: &pubsub.PubSubConfig{}}, "gcppubsub"),
		Entry("kafka", pubsub.QueueConfig{Kafka: &pubsub.KafkaConfig{}}, "kafka"),
		Entry("nats", pubsub.QueueConfig{NATS: &pubsub.NATSConfig{}}, "nats"),
	)

	It("fails for a backend that has no implementation", func() {
		_, err := pubsub.Subscribe(commonsContext.New(), pubsub.QueueConfig{RabbitMQ: &pubsub.RabbitConfig{Queue: "q"}})
		Expect(err).To(MatchError(ContainSubstring("rabbitmq backend has no implementation")))
	})

	It("dispatches to the registered opener with the config", func() {
		var got pubsub.QueueConfig
		want := pubsub.NewBatchSubscription(pubsub.BatchOptions{
			Fetch: func(context.Context) ([]*pubsub.Message, error) { return nil, nil },
			Close: func(context.Context) error { return nil },
		})
		pubsub.Register(pubsub.KindMemory, func(_ commonsContext.Context, c pubsub.QueueConfig) (pubsub.Subscription, error) {
			got = c
			return want, nil
		})

		config := pubsub.QueueConfig{Memory: &pubsub.MemoryConfig{QueueName: "events"}}
		sub, err := pubsub.Subscribe(commonsContext.New(), config)
		Expect(err).ToNot(HaveOccurred())
		Expect(sub).To(BeIdenticalTo(want))
		Expect(got).To(Equal(config))
	})
})
