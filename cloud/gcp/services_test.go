package gcp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/gcp"
)

func options(api *fakeAPI) gcp.Options {
	return gcp.Options{Endpoint: api.URL, TokenSource: staticToken(testToken)}
}

func jsonBody(request recordedRequest) map[string]any {
	var body map[string]any
	ExpectWithOffset(1, json.Unmarshal(request.Body, &body)).To(Succeed())
	return body
}

var _ = Describe("API errors", func() {
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("parses the Google JSON error envelope", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 403, `{"error":{"code":403,"message":"Permission denied","status":"PERMISSION_DENIED","errors":[{"reason":"forbidden"}]}}`)
		})
		client, err := gcp.NewKMSClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		_, err = client.Encrypt(context.Background(), "projects/p/locations/l/keyRings/r/cryptoKeys/k", []byte("x"))

		Expect(err).To(Equal(&gcp.APIError{StatusCode: 403, Code: 403, Status: "PERMISSION_DENIED", Message: "Permission denied", Reason: "forbidden"}))
		Expect(gcp.IsNotFound(err)).To(BeFalse())
	})

	It("keeps a non-JSON error body as the message", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("object not found\n"))
		})
		client, err := gcp.NewStorageClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		_, err = client.GetObject(context.Background(), "b", "o")

		Expect(err).To(Equal(&gcp.APIError{StatusCode: 404, Message: "object not found"}))
		Expect(gcp.IsNotFound(err)).To(BeTrue())
	})

	It("fails the request when the token source fails instead of sending it unauthenticated", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, 200, `{}`) })
		client, err := gcp.NewKMSClient(gcp.Options{Endpoint: api.URL, TokenSource: failingToken{}})
		Expect(err).NotTo(HaveOccurred())

		_, err = client.Encrypt(context.Background(), "projects/p/locations/l/keyRings/r/cryptoKeys/k", []byte("x"))

		Expect(err).To(MatchError(ContainSubstring("token boom")))
		Expect(api.Requests()).To(BeEmpty())
	})
})

var _ = Describe("KMS client", func() {
	const key = "projects/p/locations/global/keyRings/r/cryptoKeys/k"
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("encrypts and decrypts with base64 payloads on :encrypt and :decrypt", func() {
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Path == "/v1/"+key+":encrypt" {
				writeJSON(w, 200, `{"ciphertext":"`+base64.StdEncoding.EncodeToString([]byte("sealed"))+`"}`)
				return
			}
			writeJSON(w, 200, `{"plaintext":"`+base64.StdEncoding.EncodeToString([]byte("secret"))+`"}`)
		})
		client, err := gcp.NewKMSClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		sealed, err := client.Encrypt(context.Background(), key, []byte("secret"))
		Expect(err).NotTo(HaveOccurred())
		opened, err := client.Decrypt(context.Background(), key, sealed)
		Expect(err).NotTo(HaveOccurred())

		Expect(string(sealed)).To(Equal("sealed"))
		Expect(string(opened)).To(Equal("secret"))
		requests := api.Requests()
		Expect(requests[0].Path).To(Equal("/v1/" + key + ":encrypt"))
		Expect(jsonBody(requests[0])["plaintext"]).To(Equal(base64.StdEncoding.EncodeToString([]byte("secret"))))
		Expect(jsonBody(requests[1])["ciphertext"]).To(Equal(base64.StdEncoding.EncodeToString([]byte("sealed"))))
		Expect(requests[0].Header.Get("Authorization")).To(Equal("Bearer " + testToken))
		Expect(requests[0].Header.Get("Content-Type")).To(Equal("application/json"))
	})

	It("rejects an empty key name before calling the API", func() {
		api = newFakeAPI(func(http.ResponseWriter, *http.Request, []byte) {})
		client, err := gcp.NewKMSClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		_, err = client.Encrypt(context.Background(), "", []byte("x"))

		Expect(err).To(HaveOccurred())
		Expect(api.Requests()).To(BeEmpty())
	})
})

var _ = Describe("Container client", func() {
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("gets a cluster's endpoint and CA by project, location and name", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 200, `{"endpoint":"10.1.2.3","masterAuth":{"clusterCaCertificate":"Q0E="}}`)
		})
		client, err := gcp.NewContainerClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		cluster, err := client.GetCluster(context.Background(), gcp.ClusterRef{Project: "p", Location: "europe-west1-b", Cluster: "c"})

		Expect(err).NotTo(HaveOccurred())
		Expect(cluster.Endpoint).To(Equal("10.1.2.3"))
		Expect(cluster.MasterAuth.ClusterCaCertificate).To(Equal("Q0E="))
		Expect(api.Requests()[0].Path).To(Equal("/v1/projects/p/locations/europe-west1-b/clusters/c"))
	})

	It("rejects an incomplete cluster reference", func() {
		api = newFakeAPI(func(http.ResponseWriter, *http.Request, []byte) {})
		client, err := gcp.NewContainerClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		_, err = client.GetCluster(context.Background(), gcp.ClusterRef{Project: "p"})

		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Logging client", func() {
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("lists entries with the filter, order and page size, following page tokens", func() {
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, body []byte) {
			var in map[string]any
			Expect(json.Unmarshal(body, &in)).To(Succeed())
			if in["pageToken"] == nil {
				writeJSON(w, 200, `{"entries":[{"insertId":"1","logName":"projects/p/logs/x","severity":"WARNING","timestamp":"2024-01-02T03:04:05.5Z","textPayload":"hi","resource":{"type":"gce","labels":{"zone":"z"}},"labels":{"k":"v"}}],"nextPageToken":"t2"}`)
				return
			}
			writeJSON(w, 200, `{"entries":[{"insertId":"2","jsonPayload":{"message":"m"}},{"insertId":"3"}]}`)
		})
		client, err := gcp.NewLoggingClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		var seen []*gcp.LogEntry
		err = client.EachEntry(context.Background(), gcp.ListEntriesOptions{
			ResourceNames: []string{"projects/p"}, Filter: "severity>=ERROR", OrderBy: "timestamp desc", PageSize: 50,
		}, func(e *gcp.LogEntry) (bool, error) {
			seen = append(seen, e)
			return false, nil
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(HaveLen(3))
		Expect(seen[0].Payload()).To(Equal("hi"))
		Expect(seen[0].SeverityName()).To(Equal("Warning"))
		Expect(seen[0].Timestamp).To(Equal(time.Date(2024, 1, 2, 3, 4, 5, 500_000_000, time.UTC)))
		Expect(seen[0].Resource.Labels).To(Equal(map[string]string{"zone": "z"}))
		Expect(seen[0].Labels).To(Equal(map[string]string{"k": "v"}))
		Expect(seen[1].Payload()).To(Equal(map[string]any{"message": "m"}))
		Expect(seen[2].Payload()).To(BeNil())
		Expect(seen[2].SeverityName()).To(BeEmpty())
		requests := api.Requests()
		Expect(requests).To(HaveLen(2))
		Expect(requests[0].Path).To(Equal("/v2/entries:list"))
		Expect(jsonBody(requests[0])).To(Equal(map[string]any{
			"resourceNames": []any{"projects/p"}, "filter": "severity>=ERROR", "orderBy": "timestamp desc", "pageSize": float64(50),
		}))
		Expect(jsonBody(requests[1])["pageToken"]).To(Equal("t2"))
	})

	It("stops paging as soon as the visitor is done", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 200, `{"entries":[{"insertId":"1"},{"insertId":"2"}],"nextPageToken":"more"}`)
		})
		client, err := gcp.NewLoggingClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		count := 0
		err = client.EachEntry(context.Background(), gcp.ListEntriesOptions{ResourceNames: []string{"projects/p"}},
			func(*gcp.LogEntry) (bool, error) { count++; return true, nil })

		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(1))
		Expect(api.Requests()).To(HaveLen(1))
	})
})

var _ = Describe("Pub/Sub client", func() {
	const subscription = "projects/p/subscriptions/s"
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("pulls, acknowledges and extends ack deadlines on the subscription resource", func() {
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Path == "/v1/"+subscription+":pull" {
				writeJSON(w, 200, `{"receivedMessages":[{"ackId":"a1","deliveryAttempt":2,"message":{"data":"aGVsbG8=","attributes":{"k":"v"},"messageId":"m1","publishTime":"2024-01-02T03:04:05Z"}}]}`)
				return
			}
			writeJSON(w, 200, `{}`)
		})
		client, err := gcp.NewPubSubClient(options(api))
		Expect(err).NotTo(HaveOccurred())
		ctx := context.Background()

		messages, err := client.Pull(ctx, gcp.PullOptions{Subscription: subscription, MaxMessages: 5, ReturnImmediately: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(client.Acknowledge(ctx, subscription, []string{"a1"})).To(Succeed())
		Expect(client.ModifyAckDeadline(ctx, subscription, []string{"a1"}, 30*time.Second)).To(Succeed())

		Expect(messages).To(HaveLen(1))
		Expect(messages[0].AckID).To(Equal("a1"))
		Expect(messages[0].DeliveryAttempt).To(Equal(2))
		Expect(string(messages[0].Message.Data)).To(Equal("hello"))
		Expect(messages[0].Message.Attributes).To(Equal(map[string]string{"k": "v"}))
		Expect(messages[0].Message.PublishTime).To(Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)))
		requests := api.Requests()
		Expect(jsonBody(requests[0])).To(Equal(map[string]any{"maxMessages": float64(5), "returnImmediately": true}))
		Expect(requests[1].Path).To(Equal("/v1/" + subscription + ":acknowledge"))
		Expect(jsonBody(requests[1])["ackIds"]).To(Equal([]any{"a1"}))
		Expect(requests[2].Path).To(Equal("/v1/" + subscription + ":modifyAckDeadline"))
		Expect(jsonBody(requests[2])).To(Equal(map[string]any{"ackIds": []any{"a1"}, "ackDeadlineSeconds": float64(30)}))
	})

	It("returns no messages for an empty pull response", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, 200, `{}`) })
		client, err := gcp.NewPubSubClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		messages, err := client.Pull(context.Background(), gcp.PullOptions{Subscription: subscription, MaxMessages: 1})

		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(BeEmpty())
	})

	It("rejects a short subscription name", func() {
		api = newFakeAPI(func(http.ResponseWriter, *http.Request, []byte) {})
		client, err := gcp.NewPubSubClient(options(api))
		Expect(err).NotTo(HaveOccurred())

		Expect(client.Acknowledge(context.Background(), "s", []string{"a"})).NotTo(Succeed())
	})
})

var _ = Describe("Cloud SQL admin client", func() {
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	It("reads connect settings and requests an ephemeral cert with the access token", func() {
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.Method == http.MethodGet {
				writeJSON(w, 200, `{"region":"us-east1","backendType":"SECOND_GEN","databaseVersion":"POSTGRES_15","serverCaCert":{"cert":"CA-PEM"},"ipAddresses":[{"type":"PRIMARY","ipAddress":"1.2.3.4"},{"type":"PRIVATE","ipAddress":"10.0.0.5"}],"dnsName":"x.sql.goog"}`)
				return
			}
			writeJSON(w, 200, `{"ephemeralCert":{"cert":"CERT-PEM","expirationTime":"2030-01-01T00:00:00Z"}}`)
		})
		client, err := gcp.NewSQLAdminClient(options(api))
		Expect(err).NotTo(HaveOccurred())
		ref := gcp.SQLInstanceRef{Project: "proj", Instance: "inst"}

		settings, err := client.GetConnectSettings(context.Background(), ref)
		Expect(err).NotTo(HaveOccurred())
		cert, err := client.GenerateEphemeralCert(context.Background(), gcp.EphemeralCertRequest{SQLInstanceRef: ref, PublicKeyPEM: "PUB", AccessToken: "iam-token"})
		Expect(err).NotTo(HaveOccurred())

		Expect(settings.Region).To(Equal("us-east1"))
		Expect(settings.ServerCaCert.Cert).To(Equal("CA-PEM"))
		Expect(settings.IPAddresses).To(HaveLen(2))
		Expect(settings.IPAddresses[1].IPAddress).To(Equal("10.0.0.5"))
		Expect(cert.CertPEM).To(Equal("CERT-PEM"))
		Expect(cert.Expiration).To(Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
		requests := api.Requests()
		Expect(requests[0].Path).To(Equal("/sql/v1beta4/projects/proj/instances/inst/connectSettings"))
		Expect(requests[1].Method).To(Equal(http.MethodPost))
		Expect(requests[1].Path).To(Equal("/sql/v1beta4/projects/proj/instances/inst:generateEphemeralCert"))
		Expect(jsonBody(requests[1])).To(Equal(map[string]any{"public_key": "PUB", "access_token": "iam-token"}))
	})
})
