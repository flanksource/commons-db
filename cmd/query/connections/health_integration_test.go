package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection health endpoint", func() {
	var (
		stalling   models.Connection
		unprobable models.Connection
		handler    http.Handler
	)

	BeforeEach(func() {
		stalling = models.Connection{
			ID: uuid.New(), Name: "warehouse", Namespace: "acme", Type: models.ConnectionTypePostgres,
			URL: "postgres://operator:hunter2@" + blackholeAddress() + "/app",
		}
		unprobable = models.Connection{
			ID: uuid.New(), Name: "archive", Namespace: "acme", Type: models.ConnectionTypeFolder,
		}
		database := newConnectionTestDB([]models.Connection{stalling, unprobable})
		ctx := dbcontext.NewContext(context.Background()).WithDB(database, nil)
		handler = newConnectionHealthHandler("/api/v1", ctx, http.NotFoundHandler())
		DeferCleanup(func() {
			forgetConnectionHealth(stalling.ID.String())
			forgetConnectionHealth(unprobable.ID.String())
		})
	})

	check := func(body string) (*httptest.ResponseRecorder, connectionHealthResponse) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodPost, connectionHealthPath, strings.NewReader(body),
		))
		var response connectionHealthResponse
		if recorder.Code == http.StatusOK {
			Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		}
		return recorder, response
	}

	resultFor := func(response connectionHealthResponse, id uuid.UUID) connectionHealthItem {
		for _, item := range response.Results {
			if item.ID == id.String() {
				return item
			}
		}
		Fail("no health result for " + id.String())
		return connectionHealthItem{}
	}

	It("reports a per-connection verdict when a probe stalls, never a failed request", func() {
		recorder, response := check(`{"ids":["` + stalling.ID.String() + `","` + unprobable.ID.String() + `"]}`)

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		Expect(recorder.Body.String()).NotTo(ContainSubstring("hunter2"))
		Expect(response.Results).To(HaveLen(2))
		Expect(resultFor(response, stalling.ID).State).To(Equal(connectionHealthUnreachable))
		Expect(resultFor(response, unprobable.ID).State).To(Equal(connectionHealthUnverifiable))
	})

	It("serves a repeat check from cache and re-probes only when forced", func() {
		_, first := check(`{"ids":["` + unprobable.ID.String() + `"]}`)
		Expect(first.Results[0].Cached).To(BeFalse())

		_, repeat := check(`{"ids":["` + unprobable.ID.String() + `"]}`)
		Expect(repeat.Results[0].Cached).To(BeTrue())
		Expect(repeat.Results[0].CheckedAt).To(BeTemporally("==", first.Results[0].CheckedAt))

		time.Sleep(time.Millisecond)
		_, forced := check(`{"ids":["` + unprobable.ID.String() + `"],"force":true}`)
		Expect(forced.Results[0].Cached).To(BeFalse())
		Expect(forced.Results[0].CheckedAt).To(BeTemporally(">", first.Results[0].CheckedAt))
	})

	It("rejects a batch it cannot answer instead of returning partial results", func() {
		empty, _ := check(`{"ids":[]}`)
		Expect(empty.Code).To(Equal(http.StatusBadRequest))

		unknown := uuid.NewString()
		missing, _ := check(`{"ids":["` + unknown + `"]}`)
		Expect(missing.Code).To(Equal(http.StatusBadRequest))
		Expect(missing.Body.String()).To(ContainSubstring(unknown))

		ids := make([]string, maxHealthBatch+1)
		for index := range ids {
			ids[index] = `"` + uuid.NewString() + `"`
		}
		oversized, _ := check(`{"ids":[` + strings.Join(ids, ",") + `]}`)
		Expect(oversized.Code).To(Equal(http.StatusBadRequest))
		Expect(oversized.Body.String()).To(ContainSubstring("batch limit"))
	})

	It("passes non-health requests to the next handler", func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, connectionHealthPath, nil))

		Expect(recorder.Code).To(Equal(http.StatusNotFound))
	})
})

var _ = Describe("Kubernetes connection health", func() {
	It("reports the API server version from the saved kubeconfig", func() {
		requests := make(chan string, 1)
		apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"major":"1","minor":"31","gitVersion":"v1.31.4"}`))
			if err != nil {
				panic(err)
			}
		}))
		DeferCleanup(apiServer.Close)

		connection := models.Connection{
			ID: uuid.New(), Name: "cluster", Namespace: "acme", Type: models.ConnectionTypeKubernetes,
			Certificate: fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, apiServer.URL),
		}
		database := newConnectionTestDB([]models.Connection{connection})
		handler := newConnectionHealthHandler(
			"/api/v1",
			dbcontext.NewContext(context.Background()).WithDB(database, nil),
			http.NotFoundHandler(),
		)
		DeferCleanup(func() { forgetConnectionHealth(connection.ID.String()) })

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodPost,
			connectionHealthPath,
			strings.NewReader(`{"ids":["`+connection.ID.String()+`"]}`),
		))

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		var response connectionHealthResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		Expect(response.Results).To(HaveLen(1))
		Expect(response.Results).To(ConsistOf(connectionHealthItem{
			ID: connection.ID.String(), State: connectionHealthHealthy,
			Detail:    "Kubernetes v1.31.4",
			CheckedAt: response.Results[0].CheckedAt, DurationMS: response.Results[0].DurationMS,
		}))
		Expect(requests).To(Receive(Equal("/version")))
	})
})
