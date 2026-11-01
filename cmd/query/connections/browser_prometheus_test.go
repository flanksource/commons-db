package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	clickymetrics "github.com/flanksource/clicky/metrics"
	"github.com/flanksource/commons-db/models"
)

// A metric SQL console query maps its label columns through the request's
// labels, reports what it read, and answers with the same series projection a
// timeseries profile serves.
func TestPrometheusBrowserRunsMetricSQLAsSeries(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{"__name__":"cpu","kubernetes_pod":"api"},"values":[[1704067200,"1"]]},`+
			`{"metric":{"__name__":"cpu","kubernetes_pod":"web"},"values":[[1704067200,"2"]]}]}}`)
	}))
	defer prometheus.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypePrometheus, prometheus.URL)
	recorder := postBrowser(t, handler, base+"/query", `{"query":"SELECT time, pod, value FROM cpu WHERE pod != 'db'",
		"labels":{"pod":"kubernetes_pod"},
		"options":{"language":"sql","range":{"start":"2024-01-01T00:00:00Z","end":"2024-01-01T00:01:00Z","step":"30s"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("query status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserQueryResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	want := []clickymetrics.Series{
		{ID: "query#pod=api", Source: "query", Labels: map[string]string{"pod": "api"}, Points: []clickymetrics.Point{{At: at, Value: 1}}},
		{ID: "query#pod=web", Source: "query", Labels: map[string]string{"pod": "web"}, Points: []clickymetrics.Point{{At: at, Value: 2}}},
	}
	if !reflect.DeepEqual(result.Series, want) {
		t.Fatalf("series = %+v, want %+v", result.Series, want)
	}
	if result.Metrics == nil || len(result.Metrics.Sources) != 1 || result.Metrics.Sources[0].Expression != `{__name__="cpu",kubernetes_pod!="db"}` {
		t.Fatalf("metrics = %+v", result.Metrics)
	}
}
