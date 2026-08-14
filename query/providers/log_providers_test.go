package providers_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	_ "github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/types"
)

// connectionsDB is an in-memory connections table the providers hydrate against,
// so a spec exercises the real connection lookup rather than a stub.
func connectionsDB(conns ...models.Connection) *gorm.DB {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	Expect(err).ToNot(HaveOccurred())
	Expect(database.Exec(`CREATE TABLE connections (
id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
)`).Error).ToNot(HaveOccurred())
	for _, conn := range conns {
		Expect(database.Create(&conn).Error).ToNot(HaveOccurred())
	}
	return database
}

var _ = Describe("log provider registration", func() {
	It("registers every log backend as a provider", func() {
		for _, typ := range []string{"loki", "opensearch", "cloudwatch", "gcpcloudlogging", "bigquery", "k8s", "azureloganalytics"} {
			_, err := query.GetProvider(typ)
			Expect(err).ToNot(HaveOccurred(), "provider %q should be registered", typ)
		}
	})
})

// --- CloudWatch -------------------------------------------------------------

// cloudWatchStub answers the two calls an Insights query makes. CloudWatch Logs
// is AWS JSON 1.1 over POST /, dispatched on X-Amz-Target, so one handler serves
// both and records what StartQuery was asked for.
type cloudWatchStub struct {
	mu sync.Mutex

	startBody map[string]any
	polls     int

	// statuses are returned by successive GetQueryResults calls, so a spec can
	// walk the query through Scheduled → Running → Complete.
	statuses []string
}

func (s *cloudWatchStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch r.Header.Get("X-Amz-Target") {
		case "Logs_20140328.StartQuery":
			Expect(json.NewDecoder(r.Body).Decode(&s.startBody)).To(Succeed())
			writeAWSJSON(w, map[string]any{"queryId": "q-1"})

		case "Logs_20140328.GetQueryResults":
			status := "Complete"
			if s.polls < len(s.statuses) {
				status = s.statuses[s.polls]
			}
			s.polls++

			if status != "Complete" {
				writeAWSJSON(w, map[string]any{"status": status, "results": []any{}})
				return
			}
			writeAWSJSON(w, map[string]any{
				"status": "Complete",
				"statistics": map[string]any{
					"recordsMatched": 2, "recordsScanned": 40, "bytesScanned": 8192,
				},
				"results": []any{
					[]any{
						map[string]any{"field": "@timestamp", "value": "2026-04-19 11:23:40.207"},
						map[string]any{"field": "@message", "value": "settlement gateway rejected batch 88213"},
						map[string]any{"field": "@logStream", "value": "billing/i-0abc"},
						map[string]any{"field": "@ptr", "value": "ptr-1"},
					},
					[]any{
						map[string]any{"field": "@timestamp", "value": "2026-04-19 11:23:41.310"},
						map[string]any{"field": "@message", "value": "retry scheduled"},
						map[string]any{"field": "@logStream", "value": "billing/i-0abc"},
						map[string]any{"field": "@ptr", "value": "ptr-2"},
					},
				},
			})

		default:
			http.Error(w, "unexpected target "+r.Header.Get("X-Amz-Target"), http.StatusBadRequest)
		}
	})
}

func writeAWSJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	Expect(json.NewEncoder(w).Encode(body)).To(Succeed())
}

// awsConnection is a stored AWS connection with dummy static credentials, so
// the SDK signs requests without reaching for an ambient credential chain.
func awsConnection() models.Connection {
	return models.Connection{
		ID: uuid.New(), Name: "aws", Type: models.ConnectionTypeAWS,
		Username: "AKIAEXAMPLE", Password: "secret",
		Properties: types.JSONStringMap{"region": "eu-west-1"},
	}
}

var _ = Describe("cloudwatch provider", func() {
	var stub *cloudWatchStub
	var server *httptest.Server
	var ctx dbcontext.Context

	BeforeEach(func() {
		stub = &cloudWatchStub{}
		server = httptest.NewServer(stub.handler())
		DeferCleanup(server.Close)
		ctx = dbcontext.New().WithDB(connectionsDB(awsConnection()), nil)
	})

	execute := func(options map[string]any) ([]query.Row, error) {
		provider, err := query.GetProvider("cloudwatch")
		Expect(err).ToNot(HaveOccurred())
		options["endpoint"] = server.URL
		return provider.Execute(ctx, query.ProviderRequest{
			Connection: "connection://aws",
			Query:      "fields @timestamp, @message | sort @timestamp desc",
			Options:    options,
		})
	}

	It("runs an Insights query and maps its result fields to log rows", func() {
		rows, err := execute(map[string]any{
			"logGroup": "/aws/lambda/settlement", "start": "now-2h", "limit": "50",
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(stub.startBody).To(HaveKeyWithValue("logGroupName", "/aws/lambda/settlement"))
		Expect(stub.startBody).To(HaveKeyWithValue("queryString", "fields @timestamp, @message | sort @timestamp desc"))
		Expect(stub.startBody).To(HaveKey("startTime"))
		Expect(stub.startBody).To(HaveKey("endTime"))
		Expect(stub.startBody).To(HaveKeyWithValue("limit", BeNumerically("==", 50)))

		Expect(rows).To(HaveLen(2))
		// @message, @logStream and @ptr are the default mapping's message,
		// source and id; @ptr is an id so it must not survive as a label.
		Expect(rows[0]).To(HaveKeyWithValue("message", "settlement gateway rejected batch 88213"))
		Expect(rows[0]).To(HaveKeyWithValue("source", "billing/i-0abc"))
		Expect(rows[0]).ToNot(HaveKey("@ptr"))
		// hash is what cel.dedupe keys on, so every log provider must emit it.
		Expect(rows[0]).To(HaveKey("hash"))
	})

	It("bounds an unbounded query rather than letting StartQuery reject it", func() {
		_, err := execute(map[string]any{"logGroup": "/aws/lambda/settlement"})
		Expect(err).ToNot(HaveOccurred())
		Expect(stub.startBody).To(HaveKey("startTime"))
	})

	It("polls until the query completes", func() {
		stub.statuses = []string{"Scheduled", "Running"}
		rows, err := execute(map[string]any{"logGroup": "/aws/lambda/settlement"})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(stub.polls).To(Equal(3))
	})

	It("fails loudly when the query fails", func() {
		stub.statuses = []string{"Failed"}
		_, err := execute(map[string]any{"logGroup": "/aws/lambda/settlement"})
		Expect(err).To(HaveOccurred())
	})

	It("refuses a request with no log group instead of asking AWS for one", func() {
		_, err := execute(map[string]any{})
		Expect(err).To(MatchError(ContainSubstring("logGroup")))
		Expect(stub.startBody).To(BeNil())
	})
})

// --- Loki --------------------------------------------------------------------

var _ = Describe("loki provider authentication", func() {
	const lokiResponse = `{"status":"success","data":{"resultType":"streams","result":[` +
		`{"stream":{"app":"checkout"},"values":[["1700000000000000000","payment failed"]]}]}}`

	// The connection form stores an HTTP-family backend's credentials under
	// Properties, keyed by authType. A Loki connection configured that way used
	// to query unauthenticated, because only the username/password columns were
	// read.
	query1 := func(properties types.JSONStringMap) string {
		var authorization string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authorization = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, err := fmt.Fprint(w, lokiResponse)
			Expect(err).ToNot(HaveOccurred())
		}))
		DeferCleanup(server.Close)

		ctx := dbcontext.New().WithDB(connectionsDB(models.Connection{
			ID: uuid.New(), Name: "loki", Type: models.ConnectionTypeLoki,
			URL: server.URL, Properties: properties,
		}), nil)

		provider, err := query.GetProvider("loki")
		Expect(err).ToNot(HaveOccurred())
		rows, err := provider.Execute(ctx, query.ProviderRequest{
			Connection: "connection://loki", Query: `{app="checkout"}`,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		return authorization
	}

	It("sends the basic credentials the form stored under properties", func() {
		Expect(query1(types.JSONStringMap{
			"authType": "basic", "username": "api-user", "password": "api-password",
		})).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("api-user:api-password"))))
	})

	It("still honours credentials on the legacy username/password columns", func() {
		var authorization string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authorization = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, err := fmt.Fprint(w, lokiResponse)
			Expect(err).ToNot(HaveOccurred())
		}))
		DeferCleanup(server.Close)

		ctx := dbcontext.New().WithDB(connectionsDB(models.Connection{
			ID: uuid.New(), Name: "loki", Type: models.ConnectionTypeLoki,
			URL: server.URL, Username: "column-user", Password: "column-password",
		}), nil)

		provider, err := query.GetProvider("loki")
		Expect(err).ToNot(HaveOccurred())
		_, err = provider.Execute(ctx, query.ProviderRequest{
			Connection: "connection://loki", Query: `{app="checkout"}`,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(authorization).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("column-user:column-password"))))
	})

	It("sends a bearer token when that is what the connection carries", func() {
		Expect(query1(types.JSONStringMap{"bearer": "tok-123"})).To(Equal("Bearer tok-123"))
	})

	It("sends nothing when the connection is anonymous", func() {
		Expect(query1(types.JSONStringMap{"authType": "none"})).To(BeEmpty())
	})
})

// --- Options and connection validation --------------------------------------

var _ = Describe("log provider option validation", func() {
	ctx := dbcontext.New().WithDB(connectionsDB(), nil)

	execute := func(typ string, options map[string]any) error {
		provider, err := query.GetProvider(typ)
		Expect(err).ToNot(HaveOccurred())
		_, err = provider.Execute(ctx, query.ProviderRequest{Query: "q", Options: options})
		return err
	}

	It("requires a workspace before azureloganalytics touches the network", func() {
		Expect(execute("azureloganalytics", map[string]any{})).
			To(MatchError(ContainSubstring("workspaceID")))
	})

	It("requires a project for bigquery, naming both ways to supply it", func() {
		err := execute("bigquery", map[string]any{})
		Expect(err).To(MatchError(ContainSubstring("project")))
	})

	It("requires a project for gcpcloudlogging", func() {
		Expect(execute("gcpcloudlogging", map[string]any{})).
			To(MatchError(ContainSubstring("project")))
	})

	DescribeTable("rejects Kubernetes target options",
		func(key string, value any) {
			err := execute("k8s", map[string]any{key: value})
			Expect(err).To(MatchError(ContainSubstring("declare kind, namespace, name, uid, or labels.<key> in query")))
		},
		Entry("kind", "kind", "Deployment"),
		Entry("API version", "apiVersion", "apps/v1"),
		Entry("namespace", "namespace", "payments"),
		Entry("name", "name", "api"),
		Entry("UID", "uid", "resource-id"),
		Entry("labels", "labels", map[string]any{"app": "api"}),
	)

	It("rejects unsupported Kubernetes target fields before connecting", func() {
		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		_, err = provider.Execute(ctx, query.ProviderRequest{Query: "resource=Deployment"})
		Expect(err).To(MatchError(ContainSubstring("field \"resource\" is unsupported")))
	})
})

// --- Kubernetes -------------------------------------------------------------

// kubeconfigFor points a client at a stub API server with no credentials, which
// is all the fake needs and keeps the spec free of certificate material.
func kubeconfigFor(server string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: stub
  cluster:
    server: %s
contexts:
- name: stub
  context:
    cluster: stub
    user: stub
current-context: stub
users:
- name: stub
  user: {}
`, server)
}

var _ = Describe("k8s logs provider", func() {
	var server *httptest.Server
	var ctx dbcontext.Context
	var logRequests []string

	BeforeEach(func() {
		logRequests = nil
		mux := http.NewServeMux()

		mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "apps/v1", "kind": "DeploymentList", "items": []any{map[string]any{
					"metadata": map[string]any{"name": "billing", "namespace": "prod", "uid": "uid-billing", "labels": map[string]any{"app": "billing"}},
					"spec": map[string]any{
						"selector": map[string]any{"matchLabels": map[string]any{"app": "billing"}},
					},
				}},
			})
		})
		for _, resource := range []string{"statefulsets", "daemonsets"} {
			mux.HandleFunc("/apis/apps/v1/"+resource, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"apiVersion": "apps/v1", "items": []any{}})
			})
		}

		mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "v1", "kind": "PodList",
				"items": []any{
					podJSON("billing-1", map[string]any{"app": "billing", "role": "worker"}, "app", "sidecar"),
					podJSON("billing-2", map[string]any{"app": "billing", "role": "canary"}, "app"),
				},
			})
		})

		mux.HandleFunc("/api/v1/namespaces/prod/pods", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "v1", "kind": "PodList",
				"items": []any{
					podJSON("billing-1", map[string]any{"app": "billing", "role": "worker"}, "app", "sidecar"),
					podJSON("billing-2", map[string]any{"app": "billing", "role": "canary"}, "app"),
				},
			})
		})

		mux.HandleFunc("/api/v1/namespaces/prod/pods/", func(w http.ResponseWriter, r *http.Request) {
			logRequests = append(logRequests, r.URL.Path+"?"+r.URL.RawQuery)
			w.Header().Set("Content-Type", "text/plain")
			_, err := fmt.Fprint(w, "2026-04-19T11:23:40.207Z settlement gateway rejected batch 88213\n"+
				"2026-04-19T11:23:41.310Z retry scheduled\n")
			Expect(err).ToNot(HaveOccurred())
		})

		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)

		ctx = dbcontext.New().WithDB(connectionsDB(models.Connection{
			ID: uuid.New(), Name: "kube", Type: models.ConnectionTypeKubernetes,
			Certificate: kubeconfigFor(server.URL),
		}), nil)
	})

	execute := func(selector string, options map[string]any) ([]query.Row, error) {
		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		return provider.Execute(ctx, query.ProviderRequest{
			Connection: "connection://kube", Query: selector, Options: options,
		})
	}

	It("reads every pod of a deployment, tagging each line with where it came from", func() {
		rows, err := execute("kind=Deployment namespace=prod name=billing", map[string]any{})
		Expect(err).ToNot(HaveOccurred())

		// Two pods, two lines each, newest first.
		Expect(rows).To(HaveLen(4))
		Expect(rows[0]).To(HaveKeyWithValue("message", "retry scheduled"))
		Expect(rows[0]).To(HaveKeyWithValue("namespace", "prod"))
		Expect(rows[0]).To(HaveKeyWithValue("pod", "billing-1"))
		// No container was asked for, so the kubelet served the pod's first —
		// the row names it rather than leaving the column blank.
		Expect(rows[0]).To(HaveKeyWithValue("container", "app"))
		// The pod's own labels ride along, so a profile can column them.
		Expect(rows[0]).To(HaveKeyWithValue("role", "worker"))
	})

	It("returns the newest line first, breaking timestamp ties by stream position", func() {
		rows, err := execute("kind=Deployment namespace=prod name=billing", map[string]any{})
		Expect(err).ToNot(HaveOccurred())

		// Both pods emit the same two instants, so the timestamp alone leaves
		// every row tied with one from the other pod; the id is what settles it.
		Expect(rowValues(rows, "id")).To(Equal([]string{
			"prod/billing-1/app#1", "prod/billing-2/app#1",
			"prod/billing-1/app#0", "prod/billing-2/app#0",
		}))
	})

	It("declares an order a page can be cut from", func() {
		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		ordering, ok := provider.(query.OrderingProvider)
		Expect(ok).To(BeTrue())

		order, err := ordering.NaturalOrder(query.ProviderConfig{Type: "k8s"})
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{
			{Column: "timestamp", Desc: true},
			{Column: "id", Unique: true},
		}))
		Expect(order.Pageable()).To(Succeed())
	})

	It("reads only the containers asked for", func() {
		_, err := execute("kind=Deployment namespace=prod name=billing", map[string]any{
			"containers": []any{"sidecar"},
		})
		Expect(err).ToNot(HaveOccurred())

		// billing-1 has a sidecar, billing-2 does not.
		Expect(logRequests).To(HaveLen(1))
		Expect(logRequests[0]).To(ContainSubstring("billing-1"))
		Expect(logRequests[0]).To(ContainSubstring("container=sidecar"))
	})

	// A profile that names no connection reads the ambient cluster. When there
	// is none, NewClient hands back an empty fake clientset — which would read
	// as a healthy cluster with no pods, so every query returns zero rows and no
	// error. Saying so is the only useful answer.
	It("fails loudly when no connection is named and there is no ambient cluster", func() {
		GinkgoT().Setenv("KUBECONFIG", filepath.Join(GinkgoT().TempDir(), "absent.yaml"))
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())

		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		_, err = provider.Execute(ctx, query.ProviderRequest{
			Query: "kind=Deployment namespace=prod name=billing",
		})
		Expect(err).To(MatchError(ContainSubstring("no kubernetes cluster configured")))
	})

	It("reads the ambient cluster when no connection is named", func() {
		kubeconfig := filepath.Join(GinkgoT().TempDir(), "config.yaml")
		Expect(os.WriteFile(kubeconfig, []byte(kubeconfigFor(server.URL)), 0o600)).To(Succeed())
		GinkgoT().Setenv("KUBECONFIG", kubeconfig)

		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		rows, err := provider.Execute(ctx, query.ProviderRequest{
			Query: "kind=Deployment namespace=prod name=billing",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(4))
	})

	It("narrows a workload's pods with a resource selector", func() {
		rows, err := execute("kind=Deployment namespace=prod name=billing", map[string]any{
			"pods": []any{map[string]any{"labelSelector": "role=canary"}},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(rows).To(HaveLen(2))
		for _, row := range rows {
			Expect(row).To(HaveKeyWithValue("pod", "billing-2"))
		}
	})

	It("narrows the profile selector with runtime workload and label controls", func() {
		profile := query.Profile{
			Name: "Pod logs",
			Provider: query.ProviderConfig{
				Type: "k8s", Connection: "connection://kube",
			},
			Query: "kind=Pod namespace=prod",
		}
		result, err := query.Execute(ctx, profile, map[string]any{
			"workload": "prod/Pod/billing-1",
			"labels":   "role=worker",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		for _, row := range result.Rows {
			Expect(row).To(HaveKeyWithValue("pod", "billing-1"))
		}
	})

	It("narrows a controller scope to one of its pods without emptying the selector", func() {
		// A pod is not a Deployment, so folding this pick into the target
		// selector would AND two kinds together and match nothing. It narrows
		// the pods the Deployment resolved to instead.
		result, err := query.Execute(ctx, k8sProfile("kind=Deployment namespace=prod name=billing"), map[string]any{
			"workload": "prod/Pod/billing-2",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		for _, row := range result.Rows {
			Expect(row).To(HaveKeyWithValue("pod", "billing-2"))
		}
	})

	It("bounds the read by the generated time control, defaulting to the last hour", func() {
		_, err := query.Execute(ctx, k8sProfile("kind=Pod namespace=prod name=billing-1"), nil)
		Expect(err).ToNot(HaveOccurred())
		// Nothing was supplied, so the binding's own default is what reached the
		// kubelet — an unbounded read is never what a log query meant.
		Expect(logRequests).To(HaveLen(1))
		Expect(logRequests[0]).To(ContainSubstring("sinceTime="))
		since := sinceTimeOf(logRequests[0])
		Expect(since).To(BeTemporally("~", time.Now().Add(-time.Hour), time.Minute))

		logRequests = nil
		result, err := query.Execute(ctx, k8sProfile("kind=Pod namespace=prod name=billing-1"), map[string]any{
			"time": ">=2026-04-19T11:00:00Z,<=2026-04-19T11:23:41Z",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(sinceTimeOf(logRequests[0])).To(BeTemporally("==", time.Date(2026, 4, 19, 11, 0, 0, 0, time.UTC)))
		// The kubelet serves no upper bound, so the end is applied while
		// scanning: the 11:23:41.310 line falls outside it.
		Expect(rowValues(result.Rows, "message")).To(Equal([]string{
			"settlement gateway rejected batch 88213",
		}))
	})

	It("rejects a time bound nothing could resolve", func() {
		_, err := query.Execute(ctx, k8sProfile("kind=Pod namespace=prod"), map[string]any{
			"time": ">=yesterday",
		})
		Expect(err).To(MatchError(ContainSubstring("date math")))
	})

	It("offers the pods a scoped workload resolves to", func() {
		// The profile already names one Deployment, so the only narrowing left
		// is which of its pods to read.
		workloads, _, err := query.LookupFilterValues(
			ctx, k8sProfile("kind=Deployment namespace=prod name=billing"), nil, "workload", "", 50)
		Expect(err).ToNot(HaveOccurred())
		Expect(workloads).To(ConsistOf(
			query.FilterOption{Value: "prod/Deployment/billing", Count: 1},
			query.FilterOption{Value: "prod/Pod/billing-1", Count: 1},
			query.FilterOption{Value: "prod/Pod/billing-2", Count: 1},
		))
	})

	It("looks up workloads, grouped labels and one explicit label key inside the profile scope", func() {
		profile := query.Profile{
			Name: "Pod logs",
			Provider: query.ProviderConfig{
				Type: "k8s", Connection: "connection://kube",
			},
			Query: "kind=Pod namespace=prod",
			Params: []query.ParamDef{{
				Name: "roles", Type: query.ParamTypeLabels, Field: "labels.role",
			}},
		}
		workloads, total, err := query.LookupFilterValues(ctx, profile, nil, "workload", "", 50)
		Expect(err).ToNot(HaveOccurred())
		Expect(total).To(Equal(&query.Total{Value: 2, Exact: true}))
		Expect(workloads).To(ConsistOf(
			query.FilterOption{Value: "prod/Pod/billing-1", Count: 1},
			query.FilterOption{Value: "prod/Pod/billing-2", Count: 1},
		))

		labels, _, err := query.LookupFilterValues(ctx, profile, nil, "labels", "role=", 50)
		Expect(err).ToNot(HaveOccurred())
		Expect(labels).To(ConsistOf(
			query.FilterOption{Value: "role=canary", Count: 1},
			query.FilterOption{Value: "role=worker", Count: 1},
		))

		roles, _, err := query.LookupFilterValues(ctx, profile, nil, "roles", "", 50)
		Expect(err).ToNot(HaveOccurred())
		Expect(roles).To(ConsistOf(
			query.FilterOption{Value: "canary", Count: 1},
			query.FilterOption{Value: "worker", Count: 1},
		))
	})

	It("marks a broad result truncated when the connection resource cap selects the first match", func() {
		limited := dbcontext.New().WithDB(connectionsDB(models.Connection{
			ID: uuid.New(), Name: "limited-kube", Type: models.ConnectionTypeKubernetes,
			Certificate: kubeconfigFor(server.URL),
			Properties:  types.JSONStringMap{"max_resources": "1"},
		}), nil)
		result, err := query.Execute(limited, query.Profile{
			Name: "Limited pod logs",
			Provider: query.ProviderConfig{
				Type: "k8s", Connection: "connection://limited-kube",
			},
			Query: "kind=Pod namespace=prod",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Truncated).To(BeTrue())
		Expect(result.Rows).To(HaveLen(2))
		for _, row := range result.Rows {
			Expect(row).To(HaveKeyWithValue("pod", "billing-1"))
		}
	})
})

func podJSON(name string, labels map[string]any, containers ...string) map[string]any {
	specContainers := make([]any, len(containers))
	for i, container := range containers {
		specContainers[i] = map[string]any{"name": container, "image": "billing:1"}
	}
	return map[string]any{
		"metadata": map[string]any{
			"name": name, "namespace": "prod", "uid": "uid-" + name, "labels": labels,
		},
		"spec": map[string]any{"containers": specContainers},
	}
}

func k8sProfile(selector string) query.Profile {
	return query.Profile{
		Name:     "Pod logs",
		Provider: query.ProviderConfig{Type: "k8s", Connection: "connection://kube"},
		Query:    selector,
	}
}

func sinceTimeOf(request string) time.Time {
	_, rawQuery, _ := strings.Cut(request, "?")
	values, err := url.ParseQuery(rawQuery)
	Expect(err).ToNot(HaveOccurred())
	since, err := time.Parse(time.RFC3339, values.Get("sinceTime"))
	Expect(err).ToNot(HaveOccurred())
	return since
}

func rowValues(rows []query.Row, key string) []string {
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		value, _ := row[key].(string)
		values = append(values, value)
	}
	return values
}

func writeJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	Expect(json.NewEncoder(w).Encode(body)).To(Succeed())
}
