package cloudwatch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/logs/cloudwatch"
	"github.com/flanksource/commons/http/middlewares/awssigv4"
)

func TestCloudWatchSearch(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "logs/cloudwatch")
}

// logsServer serves StartQuery and a scripted sequence of GetQueryResults bodies.
func logsServer(results []string, startQuery *map[string]any) *cloudaws.Logs {
	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer GinkgoRecover()
		switch r.Header.Get("X-Amz-Target") {
		case "Logs_20140328.StartQuery":
			Expect(json.NewDecoder(r.Body).Decode(startQuery)).To(Succeed())
			_, _ = w.Write([]byte(`{"queryId":"q-1"}`))
		case "Logs_20140328.GetQueryResults":
			_, _ = w.Write([]byte(results[min(next, len(results)-1)]))
			next++
		default:
			Fail("unexpected target " + r.Header.Get("X-Amz-Target"))
		}
	}))
	DeferCleanup(server.Close)
	session, err := cloudaws.NewSession(cloudaws.SessionOptions{
		Region: "us-east-1", Endpoint: server.URL,
		Credentials: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"}),
	})
	Expect(err).ToNot(HaveOccurred())
	return session.Logs()
}

var _ = Describe("Searcher", func() {
	request := func() cloudwatch.Request {
		r := cloudwatch.Request{LogGroup: "grp", Query: "fields @message"}
		r.Start = "2024-05-01T10:00:00Z"
		r.End = "2024-05-01T11:00:00Z"
		r.Limit = "10"
		return r
	}

	It("starts the query in epoch seconds, polls until complete and maps rows to log lines", func(ctx SpecContext) {
		var started map[string]any
		client := logsServer([]string{
			`{"status":"Running"}`,
			`{"status":"Complete","statistics":{"recordsMatched":1},"results":[[{"field":"@ptr","value":"p1"},{"field":"@message","value":"boom"},{"field":"@timestamp","value":"2024-05-01 10:30:00.000"},{"field":"@logStream","value":"stream-a"},{"field":"@log","value":"ignored"}]]}`,
		}, &started)
		searcher := cloudwatch.New(client, nil)
		searcher.PollInterval = time.Millisecond

		result, err := searcher.Search(ctx, request())
		Expect(err).ToNot(HaveOccurred())

		Expect(started).To(Equal(map[string]any{
			"logGroupName": "grp", "queryString": "fields @message",
			"startTime": float64(1714557600), "endTime": float64(1714561200), "limit": float64(10),
		}))
		Expect(result.Logs).To(HaveLen(1))
		line := result.Logs[0]
		Expect([]any{line.ID, line.Message, line.Source, line.Count}).To(Equal([]any{"p1", "boom", "stream-a", 1}))
		Expect(line.FirstObserved).To(Equal(time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC)))
		Expect(result.Metadata["total"]).To(Equal(1))
		Expect(result.Metadata["logGroup"]).To(Equal("grp"))
	})

	DescribeTable("fails on a terminal non-complete status",
		func(ctx SpecContext, status, message string) {
			var started map[string]any
			searcher := cloudwatch.New(logsServer([]string{`{"status":"` + status + `"}`}, &started), nil)
			_, err := searcher.Search(ctx, request())
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("failed", "Failed", "failed"),
		Entry("timeout", "Timeout", "timed out"),
		Entry("cancelled", "Cancelled", "was cancelled"),
	)

	It("stops polling when the context is cancelled", func(ctx SpecContext) {
		var started map[string]any
		searcher := cloudwatch.New(logsServer([]string{`{"status":"Running"}`}, &started), nil)
		searcher.PollInterval = time.Hour
		cancelled, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		_, err := searcher.Search(cancelled, request())
		Expect(err).To(MatchError(ContainSubstring("query cancelled while waiting")))
	})
})
