package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

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

	It("rejects a k8s kind that has no pods to resolve", func() {
		err := execute("k8s", map[string]any{"kind": "CronJob", "namespace": "prod", "name": "x"})
		Expect(err).To(MatchError(ContainSubstring("kind")))
	})

	It("requires a namespace and name for k8s", func() {
		err := execute("k8s", map[string]any{"kind": "Deployment"})
		Expect(err).To(MatchError(ContainSubstring("namespace")))
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

		mux.HandleFunc("/apis/apps/v1/namespaces/prod/deployments/billing", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "billing", "namespace": "prod"},
				"spec": map[string]any{
					"selector": map[string]any{"matchLabels": map[string]any{"app": "billing"}},
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

	execute := func(options map[string]any) ([]query.Row, error) {
		provider, err := query.GetProvider("k8s")
		Expect(err).ToNot(HaveOccurred())
		return provider.Execute(ctx, query.ProviderRequest{
			Connection: "connection://kube", Options: options,
		})
	}

	It("reads every pod of a deployment, tagging each line with where it came from", func() {
		rows, err := execute(map[string]any{
			"kind": "Deployment", "namespace": "prod", "name": "billing",
		})
		Expect(err).ToNot(HaveOccurred())

		// Two pods, two lines each.
		Expect(rows).To(HaveLen(4))
		Expect(rows[0]).To(HaveKeyWithValue("message", "settlement gateway rejected batch 88213"))
		Expect(rows[0]).To(HaveKeyWithValue("namespace", "prod"))
		Expect(rows[0]).To(HaveKeyWithValue("pod", "billing-1"))
		// No container was asked for, so the kubelet served the pod's first —
		// the row names it rather than leaving the column blank.
		Expect(rows[0]).To(HaveKeyWithValue("container", "app"))
		// The pod's own labels ride along, so a profile can column them.
		Expect(rows[0]).To(HaveKeyWithValue("role", "worker"))
	})

	It("reads only the containers asked for", func() {
		_, err := execute(map[string]any{
			"kind": "Deployment", "namespace": "prod", "name": "billing",
			"containers": []any{"sidecar"},
		})
		Expect(err).ToNot(HaveOccurred())

		// billing-1 has a sidecar, billing-2 does not.
		Expect(logRequests).To(HaveLen(1))
		Expect(logRequests[0]).To(ContainSubstring("billing-1"))
		Expect(logRequests[0]).To(ContainSubstring("container=sidecar"))
	})

	It("narrows a workload's pods with a resource selector", func() {
		rows, err := execute(map[string]any{
			"kind": "Deployment", "namespace": "prod", "name": "billing",
			"pods": []any{map[string]any{"labelSelector": "role=canary"}},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(rows).To(HaveLen(2))
		for _, row := range rows {
			Expect(row).To(HaveKeyWithValue("pod", "billing-2"))
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

func writeJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	Expect(json.NewEncoder(w).Encode(body)).To(Succeed())
}
