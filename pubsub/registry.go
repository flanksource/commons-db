// Package pubsub subscribes to message queues without linking any backend.
//
// Subscribe dispatches through a registry. Each backend lives in its own
// subpackage (pubsub/sqs, pubsub/gcppubsub, pubsub/kafka, pubsub/nats) and is
// enabled explicitly by calling that subpackage's Install() once at startup;
// pubsub/all installs every backend. Nothing registers itself from init(), so
// importing pubsub alone pulls in no cloud SDK or gocloud driver.
package pubsub

import (
	"fmt"
	"sync"

	commonsContext "github.com/flanksource/commons-db/context"
)

// Kind identifies a queue backend.
type Kind string

const (
	KindSQS      Kind = "sqs"
	KindPubSub   Kind = "pubsub"
	KindKafka    Kind = "kafka"
	KindNATS     Kind = "nats"
	KindRabbitMQ Kind = "rabbitmq"
	KindMemory   Kind = "memory"
)

// Opener opens a subscription for the backend's section of a QueueConfig.
type Opener func(ctx commonsContext.Context, c QueueConfig) (Subscription, error)

// installPackage names the subpackage that provides each backend. RabbitMQ and
// memory have none: they were only ever reachable through gocloud drivers that
// this module never linked.
var installPackage = map[Kind]string{
	KindSQS:    "github.com/flanksource/commons-db/pubsub/sqs",
	KindPubSub: "github.com/flanksource/commons-db/pubsub/gcppubsub",
	KindKafka:  "github.com/flanksource/commons-db/pubsub/kafka",
	KindNATS:   "github.com/flanksource/commons-db/pubsub/nats",
}

var (
	registryMu sync.RWMutex
	openers    = map[Kind]Opener{}
)

// Register makes opener serve kind, replacing any earlier registration, so
// Install functions are idempotent.
func Register(kind Kind, opener Opener) {
	if opener == nil {
		panic(fmt.Sprintf("pubsub: Register(%q) with a nil opener", kind))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	openers[kind] = opener
}

// Kind reports which backend the config selects, in the same precedence as GetQueue.
func (c QueueConfig) Kind() (Kind, error) {
	switch {
	case c.SQS != nil:
		return KindSQS, nil
	case c.PubSub != nil:
		return KindPubSub, nil
	case c.RabbitMQ != nil:
		return KindRabbitMQ, nil
	case c.Memory != nil:
		return KindMemory, nil
	case c.Kafka != nil:
		return KindKafka, nil
	case c.NATS != nil:
		return KindNATS, nil
	}
	return "", fmt.Errorf("pubsub: no queue configuration provided")
}

// Subscribe opens the subscription selected by c through the registered backend.
func Subscribe(ctx commonsContext.Context, c QueueConfig) (Subscription, error) {
	kind, err := c.Kind()
	if err != nil {
		return nil, err
	}
	registryMu.RLock()
	opener, ok := openers[kind]
	registryMu.RUnlock()
	if ok {
		return opener(ctx, c)
	}
	pkg, ok := installPackage[kind]
	if !ok {
		return nil, fmt.Errorf("pubsub: the %s backend has no implementation", kind)
	}
	return nil, fmt.Errorf("pubsub: the %s backend is not installed: import %s and call Install() (or install every backend with github.com/flanksource/commons-db/pubsub/all)", kind, pkg)
}
