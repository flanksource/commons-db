package devtools_test

import (
	"errors"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recorderWithBody produces a finished record carrying a response body of the
// given size, which is what the detail budget is measured in.
func recorderWithBody(id string, bodySize int64) *query.Recorder {
	recorder := query.NewRecorder(query.RecorderOptions{
		ID: id, Level: logger.Trace2,
		Source: query.ExecutionSource{Surface: "profile", Profile: "orders"},
	})
	operation := recorder.Operation("opensearch")
	operation.Complete(query.OperationResult{
		Diagnostics: query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "opensearch", Query: "/_search", Detail: query.DiagnosticFull,
		}).Snapshot(),
		Entries: []har.Entry{{
			Request:  har.Request{Method: "POST", URL: "https://search.example.com/_search"},
			Response: har.Response{Status: 200, Content: har.Content{Size: bodySize}},
		}},
		Rows: 3,
	})
	recorder.Finish(query.FinishOptions{Status: 200})
	return recorder
}

var _ = Describe("Store", func() {
	It("streams a summary and holds the detail behind it", func() {
		store := devtools.NewStore(devtools.Options{})
		summary := store.Add(recorderWithBody("rec-1", 128))

		Expect(summary.Sequence).To(Equal(int64(1)))
		Expect(summary.Counts.Operations).To(Equal(1))
		Expect(store.Records(0)).To(HaveLen(1))

		detail, err := store.Detail("rec-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(detail.HAR.Log.Entries).To(HaveLen(1))
		Expect(detail.Summary.Sequence).To(Equal(int64(1)),
			"a record fetched by id must carry the sequence a client resumes from")
	})

	It("distinguishes an unknown id from detail that aged out", func() {
		store := devtools.NewStore(devtools.Options{DetailTTL: time.Nanosecond})
		store.Add(recorderWithBody("rec-1", 128))
		time.Sleep(time.Millisecond)
		store.Add(recorderWithBody("rec-2", 128))

		_, err := store.Detail("rec-1")
		var evicted *devtools.ErrDetailEvicted
		Expect(errors.As(err, &evicted)).To(BeTrue())
		Expect(evicted.Reason).To(ContainSubstring("older than"))

		_, err = store.Detail("never-existed")
		Expect(errors.As(err, &evicted)).To(BeFalse())
		Expect(err).To(MatchError(ContainSubstring("no devtools record")))
	})

	It("drops the expensive half over budget but keeps the history complete", func() {
		store := devtools.NewStore(devtools.Options{MaxDetailBytes: 1000})
		store.Add(recorderWithBody("rec-1", 800))
		store.Add(recorderWithBody("rec-2", 800))

		Expect(store.Records(0)).To(HaveLen(2), "both queries are still in the history")

		_, err := store.Detail("rec-1")
		Expect(err).To(MatchError(ContainSubstring("detail budget exceeded")))

		_, err = store.Detail("rec-2")
		Expect(err).ToNot(HaveOccurred(), "the newest detail survives")
	})

	It("reports what it dropped so a gap in sequences is explainable", func() {
		store := devtools.NewStore(devtools.Options{MaxRecords: 2})
		for _, id := range []string{"a", "b", "c", "d"} {
			store.Add(recorderWithBody(id, 8))
		}
		stats := store.Stats()
		Expect(stats.Records).To(Equal(2))
		Expect(stats.RecordsDropped).To(Equal(int64(2)))
		Expect(stats.OldestSequence).To(Equal(int64(3)))
	})

	It("keeps a process log tail resumable by sequence", func() {
		store := devtools.NewStore(devtools.Options{})
		store.Log(query.LogLine{Source: "process", Level: "info", Message: "first"})
		store.Log(query.LogLine{Source: "process", Level: "warn", Message: "second"})

		Expect(store.Logs(1)).To(HaveLen(1))
		Expect(store.Logs(1)[0].Message).To(Equal("second"))
		Expect(store.Logs(1)[0].Sequence).To(Equal(int64(2)))
	})

	It("keeps sequences climbing across a clear", func() {
		store := devtools.NewStore(devtools.Options{})
		store.Add(recorderWithBody("rec-1", 8))
		store.Clear()

		Expect(store.Records(0)).To(BeEmpty())
		Expect(store.Add(recorderWithBody("rec-2", 8)).Sequence).To(Equal(int64(2)))

		_, err := store.Detail("rec-1")
		Expect(err).To(HaveOccurred())
	})

	It("ignores a nil recorder rather than filing an empty row", func() {
		store := devtools.NewStore(devtools.Options{})
		Expect(store.Add(nil)).To(Equal(query.ExecutionSummary{}))
		Expect(store.Records(0)).To(BeEmpty())
	})
})
