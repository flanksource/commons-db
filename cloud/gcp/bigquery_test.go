package gcp_test

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/cloud/gcp"
)

type failingToken struct{}

func (failingToken) Token() (*oauth2.Token, error) { return nil, errTokenBoom }

type tokenError string

func (e tokenError) Error() string { return string(e) }

const errTokenBoom = tokenError("token boom")

const bigQuerySchemaJSON = `{"fields":[
 {"name":"s","type":"STRING","mode":"NULLABLE"},
 {"name":"i","type":"INTEGER","mode":"NULLABLE"},
 {"name":"f","type":"FLOAT","mode":"NULLABLE"},
 {"name":"b","type":"BOOLEAN","mode":"NULLABLE"},
 {"name":"ts","type":"TIMESTAMP","mode":"NULLABLE"},
 {"name":"n","type":"NUMERIC","mode":"NULLABLE"},
 {"name":"raw","type":"BYTES","mode":"NULLABLE"},
 {"name":"tags","type":"STRING","mode":"REPEATED"},
 {"name":"rec","type":"RECORD","mode":"NULLABLE","fields":[{"name":"a","type":"STRING"},{"name":"c","type":"INTEGER"}]},
 {"name":"nothing","type":"STRING","mode":"NULLABLE"}
]}`

const bigQueryRowJSON = `{"f":[{"v":"text"},{"v":"42"},{"v":"1.5"},{"v":"true"},{"v":"1.7040672E9"},{"v":"2.50"},{"v":"aGk="},
 {"v":[{"v":"x"},{"v":"y"}]},{"v":{"f":[{"v":"in"},{"v":"7"}]}},{"v":null}]}`

var _ = Describe("BigQuery client", func() {
	var api *fakeAPI
	AfterEach(func() { api.Close() })

	newClient := func() *gcp.BigQueryClient {
		client, err := gcp.NewBigQueryClient(gcp.Options{Endpoint: api.URL + "/bigquery/v2/", TokenSource: staticToken(testToken)})
		Expect(err).NotTo(HaveOccurred())
		return client
	}

	It("polls an incomplete job, pages the results and decodes every value by schema type", func() {
		job := `"jobReference":{"projectId":"p","jobId":"job-1","location":"EU"}`
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			switch {
			case r.Method == http.MethodPost:
				writeJSON(w, 200, `{"jobComplete":false,`+job+`}`)
			case r.URL.Path == "/bigquery/v2/projects/p/jobs/job-1":
				writeJSON(w, 200, `{"statistics":{"creationTime":"1700000000000","startTime":"1700000001000","endTime":"1700000003000","query":{"totalBytesProcessed":"2048","totalBytesBilled":"10485760","totalSlotMs":"77"}}}`)
			case r.URL.Query().Get("pageToken") == "":
				writeJSON(w, 200, `{"jobComplete":true,`+job+`,"totalRows":"2","pageToken":"pg2","schema":`+bigQuerySchemaJSON+`,"rows":[`+bigQueryRowJSON+`]}`)
			default:
				writeJSON(w, 200, `{"jobComplete":true,`+job+`,"totalRows":"2","schema":`+bigQuerySchemaJSON+`,"rows":[`+bigQueryRowJSON+`]}`)
			}
		})

		result, err := newClient().Query(context.Background(), gcp.QueryRequest{Project: "p", SQL: "SELECT 1"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.TotalRows).To(Equal(int64(2)))
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Schema).To(HaveLen(10))
		row := result.Rows[0]
		Expect(row[:5]).To(Equal([]any{"text", int64(42), 1.5, true, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}))
		Expect(row[5].(*big.Rat).FloatString(2)).To(Equal("2.50"))
		Expect(row[6]).To(Equal([]byte("hi")))
		Expect(row[7]).To(Equal([]any{"x", "y"}))
		Expect(row[8]).To(Equal([]any{"in", int64(7)}))
		Expect(row[9]).To(BeNil())
		Expect(result.Stats).To(Equal(&gcp.JobStatistics{
			CreationTime: time.UnixMilli(1700000000000).UTC(), StartTime: time.UnixMilli(1700000001000).UTC(), EndTime: time.UnixMilli(1700000003000).UTC(),
			BytesProcessed: 2048, BytesBilled: 10485760, SlotMillis: 77,
		}))

		requests := api.Requests()
		Expect(requests).To(HaveLen(4))
		Expect(requests[0].Path).To(Equal("/bigquery/v2/projects/p/queries"))
		body := jsonBody(requests[0])
		Expect(body["query"]).To(Equal("SELECT 1"))
		Expect(body["useLegacySql"]).To(Equal(false))
		Expect(requests[1].Query["timeoutMs"]).To(ConsistOf("10000"))
		Expect(requests[1].Query["location"]).To(ConsistOf("EU"))
		Expect(requests[2].Query["pageToken"]).To(ConsistOf("pg2"))
		Expect(requests[3].Path).To(Equal("/bigquery/v2/projects/p/jobs/job-1"))
		Expect(requests[0].Header.Get("Authorization")).To(Equal("Bearer " + testToken))
	})

	It("retries a 503 on jobs.query with the same requestId so BigQuery dedupes, and uses a new one per call", func() {
		attempts := 0
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.Method != http.MethodPost {
				writeJSON(w, 200, `{"statistics":{}}`)
				return
			}
			attempts++
			if attempts == 1 {
				writeJSON(w, 503, `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`)
				return
			}
			writeJSON(w, 200, `{"jobComplete":true,"jobReference":{"projectId":"p","jobId":"j"},"totalRows":"0","schema":{"fields":[]}}`)
		})
		client := newClient()

		for range 2 {
			_, err := client.Query(context.Background(), gcp.QueryRequest{Project: "p", SQL: "SELECT 1"})
			Expect(err).NotTo(HaveOccurred())
		}

		var ids []any
		for _, r := range api.Requests() {
			if r.Method == http.MethodPost {
				ids = append(ids, jsonBody(r)["requestId"])
			}
		}
		Expect(ids).To(HaveLen(3))
		Expect(ids[0]).To(BeAssignableToTypeOf(""))
		Expect(ids[0]).ToNot(BeEmpty())
		Expect(ids[1]).To(Equal(ids[0]), "the retry of one logical call reuses its requestId")
		Expect(ids[2]).ToNot(Equal(ids[0]), "a separate call gets its own requestId")
	})

	It("surfaces a failed query as the typed API error", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 400, `{"error":{"code":400,"message":"Syntax error","status":"INVALID_ARGUMENT","errors":[{"reason":"invalidQuery"}]}}`)
		})

		_, err := newClient().Query(context.Background(), gcp.QueryRequest{Project: "p", SQL: "SELEC"})

		var apiErr *gcp.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(apiErr.Reason).To(Equal("invalidQuery"))
		Expect(err).To(MatchError(ContainSubstring("Syntax error")))
	})

	It("fails on a value that does not match its schema type", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 200, `{"jobComplete":true,"jobReference":{"projectId":"p","jobId":"j"},"totalRows":"1","schema":{"fields":[{"name":"i","type":"INTEGER"}]},"rows":[{"f":[{"v":"notanumber"}]}]}`)
		})

		_, err := newClient().Query(context.Background(), gcp.QueryRequest{Project: "p", SQL: "SELECT 1"})

		Expect(err).To(MatchError(ContainSubstring("field i (INTEGER)")))
	})
})
