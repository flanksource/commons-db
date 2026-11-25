package gcppubsub_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/pubsub/gcppubsub"
)

func TestGCPPubSub(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "pubsub/gcppubsub")
}

const subscription = "projects/demo/subscriptions/orders"

// fakePubSub serves canned pull responses and records the other calls. No
// Pub/Sub emulator is available through deps start, so the REST API is faked.
type fakePubSub struct {
	mu       sync.Mutex
	received []map[string]any
	calls    []map[string]any
	pulls    []float64 // maxMessages of every pull request
}

func (f *fakePubSub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/"+subscription+":pull" {
		max, _ := body["maxMessages"].(float64)
		f.pulls = append(f.pulls, max)
		n := min(int(max), len(f.received))
		_ = json.NewEncoder(w).Encode(map[string]any{"receivedMessages": f.received[:n]})
		f.received = f.received[n:]
		return
	}
	body["Path"] = r.URL.Path
	f.calls = append(f.calls, body)
	_, _ = w.Write([]byte("{}"))
}

var _ = Describe("subscription against a fake Pub/Sub endpoint", func() {
	It("delivers pulled messages one at a time with attributes, then acks and nacks by ack id", func(ctx SpecContext) {
		fake := &fakePubSub{received: []map[string]any{
			{"ackId": "ack-1", "message": map[string]any{"data": "aGVsbG8=", "messageId": "m-1", "attributes": map[string]string{"origin": "billing"}}},
			{"ackId": "ack-2", "message": map[string]any{"data": "d29ybGQ=", "messageId": "m-2"}},
		}}
		server := httptest.NewServer(fake)
		DeferCleanup(server.Close)
		client, err := gcp.NewPubSubClient(gcp.Options{Endpoint: server.URL})
		Expect(err).ToNot(HaveOccurred())
		s, err := gcppubsub.New(gcppubsub.Options{Client: client, Subscription: subscription})
		Expect(err).ToNot(HaveOccurred())

		first, err := s.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(first.ID).To(Equal("m-1"))
		Expect(string(first.Body)).To(Equal("hello"))
		Expect(first.Metadata).To(Equal(map[string]string{"origin": "billing"}))
		// Only what is delivered is pulled: a buffered message would sit unacked
		// past its ack deadline while a slow handler works on the first.
		Expect(fake.pulls).To(Equal([]float64{1}))
		Expect(fake.received).To(HaveLen(1), "the second message stays on the server")

		second, err := s.Receive(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(second.Body)).To(Equal("world"))
		Expect(second.Metadata).To(BeEmpty())

		Expect(first.Ack(ctx)).To(Succeed())
		Expect(second.Nack(ctx)).To(Succeed())
		Expect(fake.calls).To(Equal([]map[string]any{
			{"Path": "/v1/" + subscription + ":acknowledge", "ackIds": []any{"ack-1"}},
			{"Path": "/v1/" + subscription + ":modifyAckDeadline", "ackIds": []any{"ack-2"}, "ackDeadlineSeconds": float64(0)},
		}))
	})

	It("requires a client and a subscription", func() {
		_, err := gcppubsub.New(gcppubsub.Options{})
		Expect(err).To(MatchError(ContainSubstring("client and subscription are required")))
	})
})
