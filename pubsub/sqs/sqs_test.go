package sqs_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons/http/middlewares/awssigv4"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/pubsub/sqs"
)

const (
	fakeQueueURL = "https://sqs.eu-west-2.amazonaws.com/123456789012/orders"
	snsEnvelope  = `{"Type":"Notification","MessageId":"sns-1","Message":"{\"order\":42}"}`
)

// fakeSQS answers ReceiveMessage with canned messages and records every other call.
type fakeSQS struct {
	mu       sync.Mutex
	messages []map[string]any
	calls    []map[string]any
	receives []float64 // MaxNumberOfMessages of every ReceiveMessage
}

func (f *fakeSQS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.")
	body["Action"] = action

	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	if action == "ReceiveMessage" {
		max, _ := body["MaxNumberOfMessages"].(float64)
		f.receives = append(f.receives, max)
		n := min(int(max), len(f.messages))
		_ = json.NewEncoder(w).Encode(map[string]any{"Messages": f.messages[:n]})
		f.messages = f.messages[n:]
		return
	}
	f.calls = append(f.calls, body)
	_, _ = w.Write([]byte("{}"))
}

func newClient(handler http.Handler) *cloudaws.SQS {
	server := httptest.NewServer(handler)
	DeferCleanup(server.Close)
	session, err := cloudaws.NewSession(cloudaws.SessionOptions{
		Region: "eu-west-2", Endpoint: server.URL,
		Credentials: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}),
	})
	Expect(err).ToNot(HaveOccurred())
	return session.SQS()
}

var _ = Describe("subscription against a fake SQS endpoint", func() {
	var fake *fakeSQS

	BeforeEach(func() {
		fake = &fakeSQS{messages: []map[string]any{
			{"MessageId": "m-1", "ReceiptHandle": "rh-1", "Body": snsEnvelope,
				"Attributes":        map[string]string{"SenderId": "AIDA", "ApproximateReceiveCount": "1"},
				"MessageAttributes": map[string]any{"trace": map[string]string{"DataType": "String", "StringValue": "abc"}}},
			{"MessageId": "m-2", "ReceiptHandle": "rh-2", "Body": "plain text"},
		}}
	})

	It("unwraps SNS envelopes, merges attributes into metadata, and acks and nacks by receipt handle", func(ctx SpecContext) {
		sub, err := sqs.New(sqs.Options{Client: newClient(fake), QueueURL: fakeQueueURL, WaitSeconds: 1})
		Expect(err).ToNot(HaveOccurred())

		first, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(first.ID).To(Equal("m-1"))
		Expect(string(first.Body)).To(Equal(`{"order":42}`))
		Expect(first.Metadata).To(Equal(map[string]string{"trace": "abc", "SenderId": "AIDA", "ApproximateReceiveCount": "1"}))
		// Only what is delivered is received: a buffered message's visibility
		// timeout would run out while a slow handler works on the first.
		Expect(fake.receives).To(Equal([]float64{1}))
		Expect(fake.messages).To(HaveLen(1), "the second message stays on the queue")

		second, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(second.Body)).To(Equal("plain text"))

		Expect(first.Ack(ctx)).To(Succeed())
		Expect(second.Nack(ctx)).To(Succeed())
		Expect(fake.calls).To(Equal([]map[string]any{
			{"Action": "DeleteMessage", "QueueUrl": fakeQueueURL, "ReceiptHandle": "rh-1"},
			{"Action": "ChangeMessageVisibility", "QueueUrl": fakeQueueURL, "ReceiptHandle": "rh-2", "VisibilityTimeout": float64(0)},
		}))
	})

	It("delivers raw bodies untouched when RawDelivery is set", func(ctx SpecContext) {
		sub, err := sqs.New(sqs.Options{Client: newClient(fake), QueueURL: fakeQueueURL, Raw: true})
		Expect(err).ToNot(HaveOccurred())

		msg, err := sub.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(msg.Body)).To(Equal(snsEnvelope))
	})

	It("rejects a long-poll wait above the SQS maximum", func() {
		_, err := sqs.New(sqs.Options{Client: newClient(fake), QueueURL: fakeQueueURL, WaitSeconds: 21})
		Expect(err).To(MatchError(ContainSubstring("outside 0..20")))
	})
})
