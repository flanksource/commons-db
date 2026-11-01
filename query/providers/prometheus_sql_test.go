package providers_test

import (
	stdctx "context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"time"

	clickymetrics "github.com/flanksource/clicky/metrics"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// prometheusRecorder is an httptest Prometheus that records each request's
// path and form and answers every query with one matrix.
type prometheusRecorder struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedPrometheusRequest
}

type recordedPrometheusRequest struct {
	Path string
	Form url.Values
}

func newPrometheusRecorder(body string) *prometheusRecorder {
	recorder := &prometheusRecorder{}
	recorder.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, recordedPrometheusRequest{Path: r.URL.Path, Form: r.Form})
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	return recorder
}

func (p *prometheusRecorder) forms() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	forms := make([]map[string]string, len(p.requests))
	for i, request := range p.requests {
		forms[i] = map[string]string{"path": request.Path}
		for key := range request.Form {
			forms[i][key] = request.Form.Get(key)
		}
	}
	return forms
}

const podMatrix = `{"status":"success","data":{"resultType":"matrix","result":[` +
	`{"metric":{"pod":"api"},"values":[[1704067200,"1"],[1704067230,"3"]]},` +
	`{"metric":{"pod":"web"},"values":[[1704067200,"2"]]}]}}`

func prometheusSQLProfile(name, url, sql string) query.Profile {
	return query.Profile{Name: name, Query: sql, Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{
		"language": "sql", "url": url,
		"range": map[string]any{"start": "2024-01-01T00:00:00Z", "end": "2024-01-01T01:00:00Z", "step": "30s"},
	}}}
}

var _ = Describe("prometheus metric SQL", func() {
	It("evaluates prometheus(query) over the profile range and exposes labels as JSON", func() {
		server := newPrometheusRecorder(podMatrix)
		defer server.Close()
		result, err := query.Execute(context.New(), prometheusSQLProfile("pods", server.URL,
			`SELECT json_extract(labels,'$.pod') AS pod, max(value) AS peak, labels FROM prometheus('sum by (pod)(rate(cpu[5m]))') GROUP BY pod ORDER BY pod`))
		Expect(err).NotTo(HaveOccurred())
		Expect(server.forms()).To(Equal([]map[string]string{{
			"path": "/api/v1/query_range", "query": "sum by (pod)(rate(cpu[5m]))",
			"start": "1704067200.000", "end": "1704070800.000", "step": "30",
		}}))
		Expect(result.Rows).To(Equal([]query.Row{
			{"pod": "api", "peak": float64(3), "labels": `{"pod":"api"}`},
			{"pod": "web", "peak": float64(2), "labels": `{"pod":"web"}`},
		}))
		Expect(result.Metrics.Sources).To(HaveLen(1))
		Expect(result.Metrics.Sources[0].Expression).To(Equal("sum by (pod)(rate(cpu[5m]))"))
		Expect(result.Metrics.Sources[0].Samples).To(Equal(3))
	})

	It("chunks a range wider than Prometheus' 11000 point limit", func() {
		server := newPrometheusRecorder(`{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		defer server.Close()
		_, err := query.Execute(context.New(), prometheusSQLProfile("wide", server.URL,
			`SELECT count(*) AS n FROM prometheus('up', '2024-01-01T00:00:00Z', '2024-01-01T05:00:00Z', '1s')`))
		Expect(err).NotTo(HaveOccurred())
		Expect(server.forms()).To(Equal([]map[string]string{
			{"path": "/api/v1/query_range", "query": "up", "start": "1704067200.000", "end": "1704077199.000", "step": "1"},
			{"path": "/api/v1/query_range", "query": "up", "start": "1704077200.000", "end": "1704085200.000", "step": "1"},
		}))
	})

	It("pushes !=, IN and LIKE on label columns into the selector", func() {
		server := newPrometheusRecorder(podMatrix)
		defer server.Close()
		profile := prometheusSQLProfile("pushdown", server.URL, `SELECT pod, count(*) AS n FROM cpu WHERE pod IN ('api','web') AND pod != 'db' AND pod LIKE 'a%' GROUP BY pod`)
		profile.Labels = map[string]string{"pod": "pod"}
		result, err := query.Execute(context.New(), profile)
		Expect(err).NotTo(HaveOccurred())
		Expect(server.forms()[0]["query"]).To(Equal(`{__name__="cpu",pod!="db",pod=~"(?i)^a.*$",pod=~"^(api|web)$"}`))
		Expect(result.Rows).To(Equal([]query.Row{{"pod": "api", "n": int64(2)}}))
	})

	It("runs the shipped pod-cpu-by-pod profile as one series per pod", func() {
		server := newPrometheusRecorder(`{"status":"success","data":{"resultType":"matrix","result":[` +
			`{"metric":{"namespace":"acme","pod":"api"},"values":[[1704067200,"0.25"]]},` +
			`{"metric":{"namespace":"other","pod":"db"},"values":[[1704067200,"0.5"]]}]}}`)
		defer server.Close()
		body, err := os.ReadFile("../../profiles/pod-cpu-by-pod.yaml")
		Expect(err).NotTo(HaveOccurred())
		var profile query.Profile
		Expect(yaml.Unmarshal(body, &profile)).To(Succeed())
		profile.Provider.Connection = ""
		profile.Provider.Options["url"] = server.URL
		result, err := query.Execute(context.New(), profile, map[string]any{"namespace": "acme"})
		Expect(err).NotTo(HaveOccurred())
		series, err := query.ProjectSeries(*result, *profile.Series)
		Expect(err).NotTo(HaveOccurred())
		Expect(series).To(Equal([]clickymetrics.Series{{
			ID: "pod-cpu-by-pod#pod=api", Source: "pod-cpu-by-pod", Label: "api", Unit: "millicores", Labels: map[string]string{"pod": "api"},
			Points: []clickymetrics.Point{{At: time.Unix(1704067200, 0).UTC(), Value: 250}},
		}}))
		Expect(profile.RenderMode()).To(Equal(query.RenderTimeseries))
	})

	It("lists label values for a column through its label alias and metric selector", func() {
		server := newPrometheusRecorder(`{"status":"success","data":["api","web","worker"]}`)
		defer server.Close()
		provider, err := query.GetProvider("prometheus")
		Expect(err).NotTo(HaveOccurred())
		lookup, ok := provider.(query.FilterLookupProvider)
		Expect(ok).To(BeTrue())
		options, total, err := lookup.LookupFilterValues(context.New(), query.ProviderRequest{
			Query: `SELECT pod FROM container_cpu`, Labels: map[string]string{"pod": "kubernetes_pod"},
			Options: map[string]any{"language": "sql", "url": server.URL},
		}, query.FilterValueSearch{Binding: query.ColumnFilterBinding{Field: "pod"}, Search: "W", Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(server.forms()).To(Equal([]map[string]string{{"path": "/api/v1/label/kubernetes_pod/values", "match[]": `{__name__="container_cpu"}`}}))
		Expect(options).To(Equal([]query.FilterOption{{Value: "web"}}))
		Expect(total).To(Equal(&query.Total{Value: 2, Exact: true}))
	})
})

var _ = Describe("profiles over profiles", func() {
	resolverFor := func(profiles ...query.Profile) query.ProfileResolver {
		return query.ProfileResolver{Resolve: func(_ stdctx.Context, name string) (query.Profile, error) {
			for _, p := range profiles {
				if p.Name == name {
					return p, nil
				}
			}
			return query.Profile{}, fmt.Errorf("profile %q not found", name)
		}}
	}

	It("runs a referenced profile with the parent's params and the child's defaults", func() {
		server := newPrometheusRecorder(podMatrix)
		defer server.Close()
		child := query.Profile{Name: "pod-cpu", Query: `sum by (pod)(rate(cpu{namespace="$(.params.namespace)",tier="$(.params.tier)"}[5m]))`,
			Params:   []query.ParamDef{{Name: "namespace", Required: true}, {Name: "tier", Default: "backend"}},
			Provider: query.ProviderConfig{Type: "prometheus", Options: map[string]any{"url": server.URL, "range": map[string]any{"start": "2024-01-01T00:00:00Z", "end": "2024-01-01T01:00:00Z", "step": "30s"}}}}
		parent := prometheusSQLProfile("pod-cpu-peak", server.URL, `SELECT pod, max(value) AS peak FROM profiles."pod-cpu" GROUP BY pod ORDER BY pod`)
		parent.Params = []query.ParamDef{{Name: "namespace"}}
		ctx := query.WithProfileResolver(context.New(), resolverFor(child))
		result, err := query.Execute(ctx, parent, map[string]any{"namespace": "acme"})
		Expect(err).NotTo(HaveOccurred())
		Expect(server.forms()[0]["query"]).To(Equal(`sum by (pod)(rate(cpu{namespace="acme",tier="backend"}[5m]))`))
		Expect(result.Rows).To(Equal([]query.Row{{"pod": "api", "peak": float64(3)}, {"pod": "web", "peak": float64(2)}}))
	})

	It("fails a reference cycle with its path", func() {
		a := prometheusSQLProfile("a", "http://127.0.0.1:1", `SELECT * FROM profiles.b`)
		b := prometheusSQLProfile("b", "http://127.0.0.1:1", `SELECT * FROM profiles.a`)
		_, err := query.Execute(query.WithProfileResolver(context.New(), resolverFor(a, b)), a)
		Expect(err).To(MatchError(ContainSubstring("profile reference cycle: a -> b -> a")))
	})

	It("refuses profiles.<name> where no resolver is bound", func() {
		_, err := query.Execute(context.New(), prometheusSQLProfile("orphan", "http://127.0.0.1:1", `SELECT * FROM profiles.b`))
		Expect(err).To(MatchError(ContainSubstring("no profile resolver")))
	})
})
