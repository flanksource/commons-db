package nats_test

import (
	"context"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/deps/start"

	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
	"github.com/flanksource/commons-db/pubsub/nats"
)

func TestNATS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "pubsub/nats")
}

var _ = Describe("NATS", Label("integration"), Ordered, func() {
	var url string

	BeforeAll(func(ctx SpecContext) {
		instances, err := start.StartServices(ctx, []start.ServiceRequest{{Name: "nats"}})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(start.StopServices(ctx, instances)).To(Succeed()) }, NodeTimeout(2*time.Minute))
		url = instances["nats"].Connection.URL
		Expect(url).To(HavePrefix("nats://"))
		nats.Install()
	}, NodeTimeout(10*time.Minute))

	It("receives a published message through Subscribe and acks it", func(ctx SpecContext) {
		sub, err := pubsub.Subscribe(dbctx.New(), pubsub.QueueConfig{NATS: &pubsub.NATSConfig{URL: url, Subject: "orders.created"}})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(sub.Shutdown(ctx)).To(Succeed()) })

		publisher, err := natsgo.Connect(url)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(publisher.Close)
		// Core NATS drops messages with no live subscriber, so keep publishing until one lands.
		go func() {
			for ctx.Err() == nil {
				_ = publisher.Publish("orders.created", []byte("order-42"))
				time.Sleep(100 * time.Millisecond)
			}
		}()

		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		msg, err := sub.Receive(receiveCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(msg.Body)).To(Equal("order-42"))
		Expect(msg.Ack(ctx)).To(Succeed())
		Expect(msg.Ack(ctx)).To(MatchError(pubsub.ErrAlreadySettled))
	})

	It("reports nack as unsupported", func(ctx SpecContext) {
		sub, err := pubsub.Subscribe(dbctx.New(), pubsub.QueueConfig{NATS: &pubsub.NATSConfig{URL: url, Subject: "orders.nack"}})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(sub.Shutdown(ctx)).To(Succeed()) })

		publisher, err := natsgo.Connect(url)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(publisher.Close)
		go func() {
			for ctx.Err() == nil {
				_ = publisher.Publish("orders.nack", []byte("x"))
				time.Sleep(100 * time.Millisecond)
			}
		}()

		receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		msg, err := sub.Receive(receiveCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(msg.Nack(ctx)).To(MatchError(pubsub.ErrNackUnsupported))
	})
})
