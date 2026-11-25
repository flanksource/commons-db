// Package sqs subscribes to AWS SQS queues over the SDK-free cloud/aws client.
package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
)

const (
	defaultWaitSeconds = 5
	maxWaitSeconds     = 20
	// receiveSize is how many messages one ReceiveMessage asks for. Messages are
	// handed out one per Receive and visibility is never extended, so anything
	// received beyond the one delivered could become visible again (and be
	// redelivered) while a slow handler is still busy. gocloud's awssnssqs
	// batched up to 10 and extended nothing either; fetching only what is
	// delivered removes that window.
	receiveSize = 1
)

// Install registers the SQS backend with pubsub.Subscribe.
func Install() { pubsub.Register(pubsub.KindSQS, Open) }

// Options configures a Subscription.
type Options struct {
	Client   *cloudaws.SQS
	QueueURL string
	// Raw delivers bodies untouched; otherwise an SNS notification envelope is unwrapped.
	Raw bool
	// WaitSeconds is the long-poll duration (0..20).
	WaitSeconds int
}

// Open subscribes to the queue described by c.SQS.
func Open(ctx commonsContext.Context, c pubsub.QueueConfig) (pubsub.Subscription, error) {
	if c.SQS == nil {
		return nil, fmt.Errorf("pubsub/sqs: QueueConfig has no sqs section")
	}
	cfg := *c.SQS
	if cfg.WaitTime == 0 {
		cfg.WaitTime = defaultWaitSeconds
	}
	if err := cfg.AWSConnection.Populate(ctx); err != nil {
		return nil, err
	}
	ctx = ctx.WithName("aws")
	session, err := cfg.AWSConnection.Client(ctx)
	if err != nil {
		return nil, err
	}
	arn, err := pubsub.ParseArn(cfg.QueueArn)
	if err != nil {
		return nil, err
	}
	ctx.Infof("Connecting to SQS queue: %s", arn.ToQueueURL())
	return New(Options{Client: session.SQS(), QueueURL: arn.ToQueueURL(), Raw: cfg.RawDelivery, WaitSeconds: cfg.WaitTime})
}

// New builds a Subscription on an existing client.
func New(o Options) (pubsub.Subscription, error) {
	if o.Client == nil || o.QueueURL == "" {
		return nil, fmt.Errorf("pubsub/sqs: a client and queue URL are required")
	}
	if o.WaitSeconds < 0 || o.WaitSeconds > maxWaitSeconds {
		return nil, fmt.Errorf("pubsub/sqs: wait time %ds is outside 0..%d", o.WaitSeconds, maxWaitSeconds)
	}
	s := &subscription{opts: o}
	return pubsub.NewBatchSubscription(pubsub.BatchOptions{
		Fetch: s.fetch,
		Close: func(context.Context) error { return nil },
	}), nil
}

type subscription struct{ opts Options }

func (s *subscription) fetch(ctx context.Context) ([]*pubsub.Message, error) {
	received, err := s.opts.Client.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{
		QueueURL:            s.opts.QueueURL,
		MaxNumberOfMessages: receiveSize,
		WaitTimeSeconds:     s.opts.WaitSeconds,
	})
	if err != nil {
		return nil, err
	}
	messages := make([]*pubsub.Message, 0, len(received))
	for _, m := range received {
		messages = append(messages, s.toMessage(m))
	}
	return messages, nil
}

func (s *subscription) toMessage(m cloudaws.Message) *pubsub.Message {
	body := []byte(m.Body)
	if !s.opts.Raw {
		body = unwrapSNSMessage(body)
	}
	return pubsub.NewMessage(pubsub.MessageOptions{
		ID:       m.MessageID,
		Body:     body,
		Metadata: metadata(m),
		Ack: func(ctx context.Context) error {
			return s.opts.Client.DeleteMessage(ctx, s.opts.QueueURL, m.ReceiptHandle)
		},
		Nack: func(ctx context.Context) error {
			return s.opts.Client.ChangeMessageVisibility(ctx, s.opts.QueueURL, m.ReceiptHandle, 0)
		},
	})
}

// metadata merges message attributes and system attributes; a system attribute
// wins a name clash.
func metadata(m cloudaws.Message) map[string]string {
	out := make(map[string]string, len(m.MessageAttributes)+len(m.Attributes))
	for k, v := range m.MessageAttributes {
		out[k] = v.StringValue
	}
	for k, v := range m.Attributes {
		out[k] = v
	}
	return out
}

// unwrapSNSMessage returns the inner Message of an SNS notification envelope,
// or body unchanged when it is not one.
func unwrapSNSMessage(body []byte) []byte {
	var envelope struct {
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Message == "" {
		return body
	}
	if unquoted, err := strconv.Unquote(envelope.Message); err == nil {
		return []byte(unquoted)
	}
	return []byte(envelope.Message)
}
