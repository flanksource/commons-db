package pubsub

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	cloudpubsub "cloud.google.com/go/pubsub"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons/logger"
	"github.com/nats-io/nats.go"
	"gocloud.dev/gcerrors"
	"gocloud.dev/pubsub"
	"gocloud.dev/pubsub/driver"
	"gocloud.dev/pubsub/kafkapubsub"
	"gocloud.dev/pubsub/natspubsub"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

func Subscribe(ctx commonsContext.Context, c QueueConfig) (*pubsub.Subscription, error) {
	if c.SQS != nil {
		if c.SQS.WaitTime == 0 {
			c.SQS.WaitTime = 5
		}
		if err := c.SQS.AWSConnection.Populate(ctx); err != nil {
			return nil, err
		}
		ctx = ctx.WithName("aws")
		ctx.Logger.SetMinLogLevel(logger.Trace)
		ctx.Logger.SetLogLevel(logger.Info)
		sess, err := c.SQS.AWSConnection.Client(ctx)
		if err != nil {
			return nil, err
		}
		arn, err := ParseArn(c.SQS.QueueArn)
		if err != nil {
			return nil, err
		}

		client := sqs.NewFromConfig(sess, func(o *sqs.Options) {
			if c.SQS.Endpoint != "" {
				o.BaseEndpoint = &c.SQS.Endpoint
			}
		})
		ctx.Infof("Connecting to SQS queue: %s", arn.ToQueueURL())

		return pubsub.NewSubscription(&sqsSubscription{
			client:   client,
			queueURL: arn.ToQueueURL(),
			raw:      c.SQS.RawDelivery,
			waitTime: time.Duration(c.SQS.WaitTime),
		}, nil, nil), nil
	}

	if c.PubSub != nil {
		if c.PubSub.ProjectID == "" || c.PubSub.Subscription == "" {
			return nil, fmt.Errorf("project_id and subscription are required for GCP Pub/Sub")
		}

		var tokenSrc oauth2.TokenSource
		var err error
		if c.PubSub.ConnectionName != "" {
			err := c.PubSub.GCPConnection.HydrateConnection(ctx)
			if err != nil {
				return nil, fmt.Errorf("error hydrating connection %s: %w", c.PubSub.ConnectionName, err)
			}
			tokenSrc, err = c.PubSub.GCPConnection.TokenSource(ctx)
			if err != nil {
				return nil, fmt.Errorf("error getting token source for %s/%s: %w", c.PubSub.ProjectID, c.PubSub.Subscription, err)
			}
		} else {
			tokenSrc, err = google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/pubsub")
			if err != nil {
				return nil, fmt.Errorf("error creating default creds for %s/%s: %w", c.PubSub.ProjectID, c.PubSub.Subscription, err)
			}
		}

		client, err := cloudpubsub.NewClient(ctx, c.PubSub.ProjectID, option.WithTokenSource(tokenSrc))
		if err != nil {
			return nil, fmt.Errorf("error connecting to GCP: %w", err)
		}

		return pubsub.NewSubscription(&gcpPubSubSubscription{
			client:       client,
			subscription: client.Subscription(c.PubSub.Subscription),
		}, nil, nil), nil
	}
	if c.Kafka != nil {
		return kafkapubsub.OpenSubscription(c.Kafka.Brokers, nil, c.Kafka.Group, []string{c.Kafka.Topic}, nil)
	}

	if c.RabbitMQ != nil {
		return pubsub.OpenSubscription(ctx, fmt.Sprintf("rabbit://%s", c.RabbitMQ.Queue))
	}

	if c.NATS != nil {
		conn, err := nats.Connect(c.NATS.URL)
		if err != nil {
			return nil, err
		}

		return natspubsub.OpenSubscriptionV2(conn, c.NATS.Subject, &natspubsub.SubscriptionOptions{
			Queue: c.NATS.Queue,
		})
	}

	if c.Memory != nil {
		return pubsub.OpenSubscription(ctx, fmt.Sprintf("mem://%s", c.Memory.QueueName))
	}

	return nil, fmt.Errorf("no queue configuration provided")
}

type sqsSubscription struct {
	client   *sqs.Client
	queueURL string
	raw      bool
	waitTime time.Duration
}

func (s *sqsSubscription) ReceiveBatch(ctx stdctx.Context, maxMessages int) ([]*driver.Message, error) {
	if maxMessages > 10 {
		maxMessages = 10
	}
	if maxMessages < 1 {
		maxMessages = 1
	}

	output, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              &s.queueURL,
		MaxNumberOfMessages:   int32(maxMessages),
		WaitTimeSeconds:       int32(s.waitTime.Seconds()),
		MessageAttributeNames: []string{"All"},
		AttributeNames:        []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil {
		return nil, err
	}

	messages := make([]*driver.Message, 0, len(output.Messages))
	for _, msg := range output.Messages {
		body := []byte(awsStringValue(msg.Body))
		if !s.raw {
			body = unwrapSNSMessage(body)
		}

		metadata := make(map[string]string, len(msg.MessageAttributes)+len(msg.Attributes))
		for k, v := range msg.MessageAttributes {
			metadata[k] = awsStringValue(v.StringValue)
		}
		for k, v := range msg.Attributes {
			metadata[string(k)] = v
		}

		receiptHandle := awsStringValue(msg.ReceiptHandle)
		messages = append(messages, &driver.Message{
			LoggableID: awsStringValue(msg.MessageId),
			Body:       body,
			Metadata:   metadata,
			AckID:      receiptHandle,
			AsFunc: func(target any) bool {
				p, ok := target.(*types.Message)
				if !ok {
					return false
				}
				*p = msg
				return true
			},
		})
	}

	return messages, nil
}

func (s *sqsSubscription) SendAcks(ctx stdctx.Context, ackIDs []driver.AckID) error {
	for _, ackID := range ackIDs {
		receiptHandle, ok := ackID.(string)
		if !ok || receiptHandle == "" {
			continue
		}
		if _, err := s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &s.queueURL, ReceiptHandle: &receiptHandle}); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqsSubscription) CanNack() bool { return true }

func (s *sqsSubscription) SendNacks(ctx stdctx.Context, ackIDs []driver.AckID) error {
	for _, ackID := range ackIDs {
		receiptHandle, ok := ackID.(string)
		if !ok || receiptHandle == "" {
			continue
		}
		_, err := s.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          &s.queueURL,
			ReceiptHandle:     &receiptHandle,
			VisibilityTimeout: 0,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *sqsSubscription) IsRetryable(err error) bool { return false }

func (s *sqsSubscription) As(i any) bool {
	client, ok := i.(**sqs.Client)
	if !ok {
		return false
	}
	*client = s.client
	return true
}

func (s *sqsSubscription) ErrorAs(err error, i any) bool { return errors.As(err, i) }

func (s *sqsSubscription) ErrorCode(err error) gcerrors.ErrorCode {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return gcerrors.Unknown
	}
	switch apiErr.ErrorCode() {
	case "AWS.SimpleQueueService.NonExistentQueue", "QueueDoesNotExist":
		return gcerrors.NotFound
	case "InvalidParameterValue", "InvalidAddress":
		return gcerrors.InvalidArgument
	case "AccessDenied", "AccessDeniedException":
		return gcerrors.PermissionDenied
	default:
		return gcerrors.Unknown
	}
}

func (s *sqsSubscription) Close() error { return nil }

func awsStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

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

type gcpPubSubSubscription struct {
	client       *cloudpubsub.Client
	subscription *cloudpubsub.Subscription
}

func (s *gcpPubSubSubscription) ReceiveBatch(ctx stdctx.Context, maxMessages int) ([]*driver.Message, error) {
	receiveCtx, cancel := stdctx.WithCancel(ctx)
	defer cancel()

	messages := make(chan *cloudpubsub.Message, 1)
	err := s.subscription.Receive(receiveCtx, func(_ stdctx.Context, msg *cloudpubsub.Message) {
		select {
		case messages <- msg:
			cancel()
		default:
			msg.Nack()
		}
	})
	if err != nil && !errors.Is(err, stdctx.Canceled) {
		return nil, err
	}

	select {
	case msg := <-messages:
		metadata := make(map[string]string, len(msg.Attributes))
		for k, v := range msg.Attributes {
			metadata[k] = v
		}

		return []*driver.Message{{
			LoggableID: msg.ID,
			Body:       msg.Data,
			Metadata:   metadata,
			AckID:      msg,
			AsFunc: func(target any) bool {
				p, ok := target.(**cloudpubsub.Message)
				if !ok {
					return false
				}
				*p = msg
				return true
			},
		}}, nil
	default:
		return nil, nil
	}
}

func (s *gcpPubSubSubscription) SendAcks(ctx stdctx.Context, ackIDs []driver.AckID) error {
	for _, ackID := range ackIDs {
		if msg, ok := ackID.(*cloudpubsub.Message); ok {
			msg.Ack()
		}
	}
	return nil
}

func (s *gcpPubSubSubscription) CanNack() bool { return true }

func (s *gcpPubSubSubscription) SendNacks(ctx stdctx.Context, ackIDs []driver.AckID) error {
	for _, ackID := range ackIDs {
		if msg, ok := ackID.(*cloudpubsub.Message); ok {
			msg.Nack()
		}
	}
	return nil
}

func (s *gcpPubSubSubscription) IsRetryable(err error) bool { return false }

func (s *gcpPubSubSubscription) As(i any) bool {
	client, ok := i.(**cloudpubsub.Client)
	if !ok {
		return false
	}
	*client = s.client
	return true
}

func (s *gcpPubSubSubscription) ErrorAs(err error, i any) bool { return errors.As(err, i) }

func (s *gcpPubSubSubscription) ErrorCode(err error) gcerrors.ErrorCode {
	return gcerrors.Unknown
}

func (s *gcpPubSubSubscription) Close() error { return s.client.Close() }
