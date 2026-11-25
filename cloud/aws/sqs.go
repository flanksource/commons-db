package aws

import "context"

var sqsProtocol = jsonProtocol{Version: "1.0", TargetPrefix: "AmazonSQS"}

// SQS is the Simple Queue Service client (JSON 1.0).
type SQS struct{ svc *service }

// SQS returns the SQS client.
func (s *Session) SQS() *SQS { return &SQS{svc: s.newService("sqs", "sqs", "sqs")} }

// ReceiveMessageInput polls a queue. All message attributes and system
// attributes are always requested.
type ReceiveMessageInput struct {
	QueueURL            string
	MaxNumberOfMessages int
	// WaitTimeSeconds enables long polling when positive.
	WaitTimeSeconds int
	// VisibilityTimeout overrides the queue's timeout when positive.
	VisibilityTimeout int
}

// MessageAttribute is a user-defined message attribute.
type MessageAttribute struct {
	DataType    string `json:"DataType"`
	StringValue string `json:"StringValue"`
}

// Message is a received SQS message.
type Message struct {
	MessageID         string                      `json:"MessageId"`
	ReceiptHandle     string                      `json:"ReceiptHandle"`
	Body              string                      `json:"Body"`
	Attributes        map[string]string           `json:"Attributes"`
	MessageAttributes map[string]MessageAttribute `json:"MessageAttributes"`
}

type receiveMessageRequest struct {
	QueueURL                    string   `json:"QueueUrl"`
	MaxNumberOfMessages         int      `json:"MaxNumberOfMessages,omitempty"`
	WaitTimeSeconds             int      `json:"WaitTimeSeconds,omitempty"`
	VisibilityTimeout           int      `json:"VisibilityTimeout,omitempty"`
	MessageAttributeNames       []string `json:"MessageAttributeNames"`
	MessageSystemAttributeNames []string `json:"MessageSystemAttributeNames"`
}

// ReceiveMessage long-polls the queue.
func (c *SQS) ReceiveMessage(ctx context.Context, in ReceiveMessageInput) ([]Message, error) {
	req := receiveMessageRequest{
		QueueURL:                    in.QueueURL,
		MaxNumberOfMessages:         in.MaxNumberOfMessages,
		WaitTimeSeconds:             in.WaitTimeSeconds,
		VisibilityTimeout:           in.VisibilityTimeout,
		MessageAttributeNames:       []string{"All"},
		MessageSystemAttributeNames: []string{"All"},
	}
	var out struct {
		Messages []Message `json:"Messages"`
	}
	if err := c.svc.jsonRPC(ctx, sqsProtocol, "ReceiveMessage", req, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// DeleteMessage acknowledges a message by its receipt handle.
func (c *SQS) DeleteMessage(ctx context.Context, queueURL, receiptHandle string) error {
	in := struct {
		QueueURL      string `json:"QueueUrl"`
		ReceiptHandle string `json:"ReceiptHandle"`
	}{queueURL, receiptHandle}
	return c.svc.jsonRPC(ctx, sqsProtocol, "DeleteMessage", in, nil)
}

// ChangeMessageVisibility sets how long a received message stays hidden; zero
// makes it visible again immediately (a nack).
func (c *SQS) ChangeMessageVisibility(ctx context.Context, queueURL, receiptHandle string, visibilityTimeoutSeconds int) error {
	in := struct {
		QueueURL          string `json:"QueueUrl"`
		ReceiptHandle     string `json:"ReceiptHandle"`
		VisibilityTimeout int    `json:"VisibilityTimeout"`
	}{queueURL, receiptHandle, visibilityTimeoutSeconds}
	return c.svc.jsonRPC(ctx, sqsProtocol, "ChangeMessageVisibility", in, nil)
}

// CreateQueue creates a standard queue (or returns the existing one) and returns its URL.
func (c *SQS) CreateQueue(ctx context.Context, name string) (string, error) {
	in := struct {
		QueueName string `json:"QueueName"`
	}{name}
	var out struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := c.svc.jsonRPC(ctx, sqsProtocol, "CreateQueue", in, &out); err != nil {
		return "", err
	}
	return out.QueueURL, nil
}

// SendMessage enqueues a message body and returns its id.
func (c *SQS) SendMessage(ctx context.Context, queueURL, body string) (string, error) {
	in := struct {
		QueueURL    string `json:"QueueUrl"`
		MessageBody string `json:"MessageBody"`
	}{queueURL, body}
	var out struct {
		MessageID string `json:"MessageId"`
	}
	if err := c.svc.jsonRPC(ctx, sqsProtocol, "SendMessage", in, &out); err != nil {
		return "", err
	}
	return out.MessageID, nil
}
