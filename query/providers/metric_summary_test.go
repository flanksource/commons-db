package providers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("structured metric profiles", func() {
	It("preserves Prometheus warning and info annotations", func() {
		server := jsonServer(200, `{"status":"success","warnings":["source warning"],"infos":["source info"],"data":{"resultType":"vector","result":[{"metric":{"pod":"a"},"value":[1700000000,"1"]}]}}`)
		defer server.Close()
		result, err := query.Execute(context.New(), query.Profile{Name: "annotated", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"url": server.URL}}, Query: "up"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Metrics).NotTo(BeNil())
		Expect(result.Metrics.Notices).To(Equal([]query.QueryNotice{{Severity: "warning", Message: "source warning"}, {Severity: "info", Message: "source info"}}))
	})
	It("rejects native histograms rather than emitting zero values", func() {
		server := jsonServer(200, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"a"},"histogram":[1700000000,{"count":"2","sum":"1"}]}]}}`)
		defer server.Close()
		_, err := query.Execute(context.New(), query.Profile{Name: "histogram", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"url": server.URL}}, Query: "requests"})
		Expect(err).To(MatchError(ContainSubstring("native histograms")))
	})
	It("runs the shipped container and pod profiles as numeric summary tables", func() {
		var calls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			expression := r.Form.Get("query")
			value := "1048576"
			if strings.Contains(expression, "container_cpu") {
				if !strings.HasPrefix(expression, "rate(") {
					http.Error(w, "expected native rate", 400)
					return
				}
				value = "0.1"
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"cluster":"test","namespace":"acme","pod":"app","container":"worker","job":"kubelet"},"values":[[1700000000,"%s"],[1700000030,"%s"]]}]}}`, value, value)
		}))
		defer server.Close()
		for _, name := range []string{"container-metrics", "pod-metrics"} {
			body, err := os.ReadFile("../../profiles/" + name + ".yaml")
			Expect(err).NotTo(HaveOccurred())
			var profile query.Profile
			Expect(yaml.Unmarshal(body, &profile)).To(Succeed())
			profile.Provider.Connection = ""
			profile.Provider.Options["url"] = server.URL
			result, err := query.Execute(context.New(), profile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Rows).To(HaveLen(2))
			for _, row := range result.Rows {
				for _, stat := range []string{"min", "avg", "max", "p50", "p90", "p95", "p99"} {
					Expect(row[stat]).To(BeAssignableToTypeOf(float64(0)))
				}
				Expect(row["samples"]).To(BeNumerically("==", 2))
			}
		}
		Expect(calls.Load()).To(Equal(int64(4)))
	})
	It("resumes a summary snapshot without querying Prometheus again", func() {
		var calls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"pod":"a"},"values":[[1700000000,"1"]]},{"metric":{"pod":"b"},"values":[[1700000000,"2"]]}]}}`)
		}))
		defer server.Close()
		profile := query.Profile{Name: "paged-metrics", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"language": "sql", "url": server.URL, "range": map[string]any{"start": "now-1h", "end": "now", "step": "30s"}}}, Labels: map[string]string{"pod": "pod"}, Query: `SELECT pod,avg(value) AS avg FROM up GROUP BY pod`, Order: query.Order{{Column: "pod", Unique: true}}}
		var cursor query.Cursor
		for page, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1}) {
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Rows[0]["pod"]).To(Equal("a"))
			cursor = page.Next
			break
		}
		Expect(cursor).NotTo(BeEmpty())
		for page, err := range query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1, Cursor: cursor}) {
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Rows[0]["pod"]).To(Equal("b"))
			Expect(page.HasMore).To(BeFalse())
		}
		Expect(calls.Load()).To(Equal(int64(1)))
	})
	It("binds SQL parameters as values", func() {
		server := jsonServer(200, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		defer server.Close()
		_, err := query.Execute(context.New(), query.Profile{Name: "bound-metrics", Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"url": server.URL, "language": "sql", "range": map[string]any{"start": "now-1h", "end": "now", "step": "30s"}}}, Labels: map[string]string{"pod": "pod"}, Params: []query.ParamDef{{Name: "pod"}}, Query: `SELECT pod,avg(value) AS avg FROM up WHERE pod = $(.params.pod) GROUP BY pod`}, map[string]any{"pod": "x' OR 1=1 --"})
		Expect(err).NotTo(HaveOccurred())
	})
})
