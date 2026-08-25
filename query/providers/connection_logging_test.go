package providers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	clickytext "github.com/flanksource/clicky/text"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection logging through HTTP providers", func() {
	It("emits cumulative sanitized HTTP details at trace2", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Authorization", "Bearer response-token")
			_, _ = w.Write([]byte(`{"token":"hunter2","id":7}`))
		}))
		defer server.Close()

		buffer := logger.NewBufferedLogger(20)
		buffer.SetLogLevel(logger.Trace2)
		ctx := dbcontext.New(commons.WithLogger(buffer))

		result, err := query.Execute(ctx, query.Profile{
			Name: "http logging",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"url": server.URL},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"token": "hunter2", "id": float64(7)}}))

		messages := make([]string, len(buffer.GetLogs()))
		for index, entry := range buffer.GetLogs() {
			messages[index] = entry.Message
		}
		joined := clickytext.StripANSI(strings.Join(messages, "\n"))
		Expect(joined).To(And(
			ContainSubstring("[http/inline]"),
			ContainSubstring("GET "+server.URL),
			ContainSubstring("200 OK"),
			ContainSubstring("Request Headers"),
			ContainSubstring("Response Headers"),
			ContainSubstring("Response Body"),
			ContainSubstring(`"token": "h****"`),
			Not(ContainSubstring("hunter2")),
			Not(ContainSubstring("response-token")),
		))
	})
})

var _ = Describe("Connection logging through Kubernetes", func() {
	It("includes the resolved selector, pods, and time range in a slow log", func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "apps/v1", "kind": "DeploymentList", "items": []any{map[string]any{
					"metadata": map[string]any{"name": "billing", "namespace": "prod", "uid": "uid-billing", "labels": map[string]any{"app": "billing"}},
					"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "billing"}}},
				}},
			})
		})
		for _, resource := range []string{"statefulsets", "daemonsets"} {
			mux.HandleFunc("/apis/apps/v1/"+resource, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, map[string]any{"apiVersion": "apps/v1", "items": []any{}})
			})
		}
		mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "v1", "kind": "PodList", "items": []any{
					podJSON("billing-1", map[string]any{"app": "billing"}, "app"),
					podJSON("billing-2", map[string]any{"app": "billing"}, "app"),
				},
			})
		})
		mux.HandleFunc("/api/v1/namespaces/prod/pods", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{
				"apiVersion": "v1", "kind": "PodList", "items": []any{
					podJSON("billing-1", map[string]any{"app": "billing"}, "app"),
					podJSON("billing-2", map[string]any{"app": "billing"}, "app"),
				},
			})
		})
		mux.HandleFunc("/api/v1/namespaces/prod/pods/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			for _, line := range podLogLines {
				_, err := fmt.Fprintln(w, line)
				Expect(err).ToNot(HaveOccurred())
			}
		})
		server := httptest.NewServer(mux)
		DeferCleanup(server.Close)

		buffer := logger.NewBufferedLogger(20)
		buffer.SetLogLevel(logger.Warn)
		ctx := dbcontext.New(commons.WithLogger(buffer)).WithDB(connectionsDB(models.Connection{
			ID: uuid.New(), Name: "kube", Type: models.ConnectionTypeKubernetes,
			Certificate: kubeconfigFor(server.URL),
			Properties: types.JSONStringMap{
				observability.PropertySlowThreshold: "1ns",
			},
		}), nil)

		result, err := query.Execute(ctx, k8sProfile("kind=Deployment namespace=prod name=billing"), map[string]any{
			"time":     fixtureLogWindow,
			"workload": "prod/Pod/billing-2",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))

		messages := make([]string, len(buffer.GetLogs()))
		for index, entry := range buffer.GetLogs() {
			messages[index] = entry.Message
		}
		joined := clickytext.StripANSI(strings.Join(messages, "\n"))
		Expect(joined).To(And(
			ContainSubstring("[k8s/kube] SLOW >= "),
			ContainSubstring("kind=Deployment namespace=prod name=billing"),
			ContainSubstring("filters="),
			ContainSubstring("namespaces"),
			ContainSubstring("pods"),
			ContainSubstring("start"),
			ContainSubstring("end"),
			ContainSubstring("prod/billing-2"),
			ContainSubstring("2026-04-19T11:00:00Z"),
			ContainSubstring("2026-04-19T12:00:00Z"),
		))
	})
})
