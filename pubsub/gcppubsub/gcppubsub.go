// Package gcppubsub subscribes to GCP Pub/Sub over the SDK-free cloud/gcp REST client.
package gcppubsub

import (
	"context"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/connection"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pubsub"
)

// pullSize is how many messages one pull asks for. Messages are handed out one
// per Receive and the ack deadline is never extended, so anything pulled beyond
// the one delivered would expire (default 10s) while a slow handler runs and be
// redelivered.
const pullSize = 1

// Install registers the GCP Pub/Sub backend with pubsub.Subscribe.
func Install() { pubsub.Register(pubsub.KindPubSub, Open) }

// Options configures a Subscription.
type Options struct {
	Client *gcp.PubSubClient
	// Subscription is the full resource name, projects/P/subscriptions/S.
	Subscription string
}

// Open subscribes to the subscription described by c.PubSub.
func Open(ctx commonsContext.Context, c pubsub.QueueConfig) (pubsub.Subscription, error) {
	if c.PubSub == nil {
		return nil, fmt.Errorf("pubsub/gcppubsub: QueueConfig has no pubsub section")
	}
	cfg := *c.PubSub
	if cfg.ProjectID == "" || cfg.Subscription == "" {
		return nil, fmt.Errorf("project_id and subscription are required for GCP Pub/Sub")
	}
	opts, err := clientOptions(ctx, &cfg.GCPConnection)
	if err != nil {
		return nil, fmt.Errorf("error creating credentials for %s/%s: %w", cfg.ProjectID, cfg.Subscription, err)
	}
	client, err := gcp.NewPubSubClient(opts)
	if err != nil {
		return nil, fmt.Errorf("error connecting to GCP: %w", err)
	}
	return New(Options{Client: client, Subscription: fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, cfg.Subscription)})
}

// clientOptions authenticates with the named connection's credentials, or with
// the ambient application default credentials. Emulators (an explicit endpoint
// or PUBSUB_EMULATOR_HOST) take no credentials.
func clientOptions(ctx commonsContext.Context, conn *connection.GCPConnection) (gcp.Options, error) {
	if conn.ConnectionName != "" {
		if err := conn.HydrateConnection(ctx); err != nil {
			return gcp.Options{}, fmt.Errorf("error hydrating connection %s: %w", conn.ConnectionName, err)
		}
		tokenSource, err := conn.TokenSource(ctx, gcp.ScopePubSub)
		if err != nil {
			return gcp.Options{}, err
		}
		return gcp.Options{Endpoint: conn.Endpoint, SkipTLSVerify: conn.SkipTLSVerify, TokenSource: tokenSource}, nil
	}
	opts := gcp.Options{Endpoint: conn.Endpoint, SkipTLSVerify: conn.SkipTLSVerify}
	if opts.Endpoint != "" || os.Getenv(gcp.PubSubEmulatorHostEnv) != "" {
		return opts, nil
	}
	tokenSource, err := gcp.DefaultTokenSource(ctx, gcp.ScopePubSub)
	if err != nil {
		return gcp.Options{}, err
	}
	opts.TokenSource = tokenSource
	return opts, nil
}

// New builds a Subscription on an existing client.
func New(o Options) (pubsub.Subscription, error) {
	if o.Client == nil || o.Subscription == "" {
		return nil, fmt.Errorf("pubsub/gcppubsub: a client and subscription are required")
	}
	s := &subscription{opts: o}
	return pubsub.NewBatchSubscription(pubsub.BatchOptions{
		Fetch: s.fetch,
		Close: func(context.Context) error { return nil },
	}), nil
}

type subscription struct{ opts Options }

func (s *subscription) fetch(ctx context.Context) ([]*pubsub.Message, error) {
	received, err := s.opts.Client.Pull(ctx, gcp.PullOptions{Subscription: s.opts.Subscription, MaxMessages: pullSize})
	if err != nil {
		return nil, err
	}
	messages := make([]*pubsub.Message, 0, len(received))
	for _, r := range received {
		messages = append(messages, s.toMessage(r))
	}
	return messages, nil
}

func (s *subscription) toMessage(r gcp.ReceivedMessage) *pubsub.Message {
	attributes := make(map[string]string, len(r.Message.Attributes))
	maps.Copy(attributes, r.Message.Attributes)
	return pubsub.NewMessage(pubsub.MessageOptions{
		ID:       r.Message.MessageID,
		Body:     r.Message.Data,
		Metadata: attributes,
		Ack: func(ctx context.Context) error {
			return s.opts.Client.Acknowledge(ctx, s.opts.Subscription, []string{r.AckID})
		},
		Nack: func(ctx context.Context) error {
			return s.opts.Client.ModifyAckDeadline(ctx, s.opts.Subscription, []string{r.AckID}, time.Duration(0))
		},
	})
}
