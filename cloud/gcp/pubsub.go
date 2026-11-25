package gcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	pubsubDefaultRoot = "https://pubsub.googleapis.com"
	pubsubAPIPath     = "/v1"

	// PubSubEmulatorHostEnv is honoured when no endpoint is configured, matching
	// the convention used by the Google Cloud Pub/Sub client libraries.
	PubSubEmulatorHostEnv = "PUBSUB_EMULATOR_HOST"
)

// PubSubClient talks to the Pub/Sub REST API for subscribers.
type PubSubClient struct{ *api }

// NewPubSubClient builds a Pub/Sub client. With no Endpoint it uses
// PUBSUB_EMULATOR_HOST when set, else pubsub.googleapis.com.
func NewPubSubClient(opts Options) (*PubSubClient, error) {
	if opts.Endpoint == "" {
		if host := os.Getenv(PubSubEmulatorHostEnv); host != "" {
			opts.Endpoint = "http://" + host
			if strings.Contains(host, "://") {
				opts.Endpoint = host
			}
		}
	}
	client, err := newAPI(opts, pubsubDefaultRoot, pubsubAPIPath)
	if err != nil {
		return nil, err
	}
	return &PubSubClient{api: client}, nil
}

// PubSubMessage is a delivered message.
type PubSubMessage struct {
	Data        []byte            `json:"data"`
	Attributes  map[string]string `json:"attributes"`
	MessageID   string            `json:"messageId"`
	PublishTime time.Time         `json:"publishTime"`
	OrderingKey string            `json:"orderingKey"`
}

// ReceivedMessage is a message with the ack ID used to acknowledge it.
type ReceivedMessage struct {
	AckID           string        `json:"ackId"`
	Message         PubSubMessage `json:"message"`
	DeliveryAttempt int           `json:"deliveryAttempt"`
}

// PullOptions configures a pull. Subscription is the full resource name
// (projects/P/subscriptions/S).
type PullOptions struct {
	Subscription string
	MaxMessages  int
	// ReturnImmediately returns an empty response instead of waiting for messages.
	ReturnImmediately bool
}

// Pull retrieves up to MaxMessages messages.
func (p *PubSubClient) Pull(ctx context.Context, o PullOptions) ([]ReceivedMessage, error) {
	if o.MaxMessages <= 0 {
		return nil, fmt.Errorf("gcp: pull from %s needs MaxMessages > 0", o.Subscription)
	}
	body := map[string]any{"maxMessages": o.MaxMessages, "returnImmediately": o.ReturnImmediately}
	var out struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	if err := p.subscriptionCall(ctx, o.Subscription, "pull", body, &out); err != nil {
		return nil, err
	}
	return out.ReceivedMessages, nil
}

// Acknowledge acknowledges messages by ack ID.
func (p *PubSubClient) Acknowledge(ctx context.Context, subscription string, ackIDs []string) error {
	return p.subscriptionCall(ctx, subscription, "acknowledge", map[string]any{"ackIds": ackIDs}, nil)
}

// ModifyAckDeadline sets the ack deadline of messages; 0 nacks them.
func (p *PubSubClient) ModifyAckDeadline(ctx context.Context, subscription string, ackIDs []string, deadline time.Duration) error {
	body := map[string]any{"ackIds": ackIDs, "ackDeadlineSeconds": int(deadline / time.Second)}
	return p.subscriptionCall(ctx, subscription, "modifyAckDeadline", body, nil)
}

func (p *PubSubClient) subscriptionCall(ctx context.Context, subscription, verb string, body, out any) error {
	if !strings.HasPrefix(subscription, "projects/") {
		return fmt.Errorf("gcp: pubsub %s: subscription %q is not a full resource name (projects/P/subscriptions/S)", verb, subscription)
	}
	return p.do(ctx, call{Method: http.MethodPost, URL: p.base + "/" + subscription + ":" + verb, Body: body, Out: out})
}
