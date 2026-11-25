// Package all installs every pubsub backend. Importing it links the SDK-free
// SQS and Pub/Sub clients plus the gocloud Kafka and NATS drivers; import the
// individual backend packages instead to keep the dependency graph small.
package all

import (
	"github.com/flanksource/commons-db/pubsub/gcppubsub"
	"github.com/flanksource/commons-db/pubsub/kafka"
	"github.com/flanksource/commons-db/pubsub/nats"
	"github.com/flanksource/commons-db/pubsub/sqs"
)

// Install registers SQS, GCP Pub/Sub, Kafka and NATS with pubsub.Subscribe.
func Install() {
	sqs.Install()
	gcppubsub.Install()
	kafka.Install()
	nats.Install()
}
