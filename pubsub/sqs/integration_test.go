package sqs_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/cloud/aws/awstest"
	"github.com/flanksource/commons-db/connection"
	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
	"github.com/flanksource/commons-db/pubsub/sqs"
	"github.com/flanksource/commons-db/types"
)

// These specs run against the ministack emulator, started in Docker (possibly
// on a remote host) and addressed through the endpoint it reports.
var _ = Describe("SQS on ministack", Label("integration"), Ordered, func() {
	var (
		emulator *awstest.Emulator
		client   *cloudaws.SQS
		config   pubsub.QueueConfig
		queueURL string
	)

	BeforeAll(func(ctx SpecContext) {
		var err error
		emulator, err = awstest.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(emulator.Stop(ctx)).To(Succeed()) }, NodeTimeout(2*time.Minute))

		session, err := cloudaws.NewSession(emulator.SessionOptions())
		Expect(err).ToNot(HaveOccurred())
		client = session.SQS()

		name := fmt.Sprintf("pubsub-%d", time.Now().UnixNano())
		queueURL, err = client.CreateQueue(ctx, name)
		Expect(err).ToNot(HaveOccurred())
		sqs.Install()
		config = pubsub.QueueConfig{SQS: &pubsub.SQSConfig{
			QueueArn:    queueARN(emulator.Region, queueURL),
			RawDelivery: true,
			WaitTime:    1,
			AWSConnection: connection.AWSConnection{
				Endpoint: emulator.Endpoint, Region: emulator.Region,
				AccessKey: types.EnvVar{ValueStatic: emulator.AccessKey}, SecretKey: types.EnvVar{ValueStatic: emulator.SecretKey},
			},
		}}
	}, NodeTimeout(10*time.Minute))

	It("receives a message and acking it deletes it from the queue", func(ctx SpecContext) {
		sub, err := pubsub.Subscribe(dbctx.New(), config)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(sub.Shutdown(ctx)).To(Succeed()) })

		sent, err := client.SendMessage(ctx, queueURL, "hello ack")
		Expect(err).ToNot(HaveOccurred())

		msg := receiveWithin(ctx, sub, 30*time.Second)
		Expect(msg.ID).To(Equal(sent))
		Expect(string(msg.Body)).To(Equal("hello ack"))
		Expect(msg.Metadata).To(HaveKey("ApproximateReceiveCount"))
		Expect(msg.Ack(ctx)).To(Succeed())

		Consistently(func() int { return len(pollOnce(ctx, client, queueURL)) }, 3*time.Second, time.Second).Should(BeZero())
	})

	It("nacking a message makes it visible again immediately", func(ctx SpecContext) {
		sub, err := pubsub.Subscribe(dbctx.New(), config)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(sub.Shutdown(ctx)).To(Succeed()) })

		sent, err := client.SendMessage(ctx, queueURL, "hello nack")
		Expect(err).ToNot(HaveOccurred())

		first := receiveWithin(ctx, sub, 30*time.Second)
		Expect(first.ID).To(Equal(sent))
		Expect(first.Nack(ctx)).To(Succeed())

		again := receiveWithin(ctx, sub, 10*time.Second)
		Expect(again.ID).To(Equal(sent))
		Expect(again.Metadata["ApproximateReceiveCount"]).To(Equal("2"))
		Expect(again.Ack(ctx)).To(Succeed())
	})
})

// queueARN rebuilds the ARN of the queue behind url (https://host/<account>/<name>).
func queueARN(region, url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", region, parts[len(parts)-2], parts[len(parts)-1])
}

func receiveWithin(ctx context.Context, sub pubsub.Subscription, d time.Duration) *pubsub.Message {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	msg, err := sub.Receive(ctx)
	Expect(err).ToNot(HaveOccurred())
	return msg
}

func pollOnce(ctx context.Context, client *cloudaws.SQS, queueURL string) []cloudaws.Message {
	GinkgoHelper()
	messages, err := client.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{QueueURL: queueURL, MaxNumberOfMessages: 1, VisibilityTimeout: 1})
	Expect(err).ToNot(HaveOccurred())
	return messages
}
