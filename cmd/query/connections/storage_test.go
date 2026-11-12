package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/connection/store"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection storage destinations", func() {
	It("decodes the HTTP body once before merging properties and moving layers", func() {
		layers := newLayeredStore()
		provider, err := NewStoreProvider(StoreProviderOptions{Store: layers.store, WriteLayer: "app", WritableLayers: []string{"app", "global"}})
		Expect(err).NotTo(HaveOccurred())
		service := newStoreService(provider, Policy{})
		created, err := service.Create(context.Background(), map[string]any{"name": "orders", "type": "postgres", "url": "postgres://db.example.com/app", "password": "credential-canary"})
		Expect(err).NotTo(HaveOccurred())
		service.decodeBody = profiles.DecodeRequestBody
		request := httptest.NewRequest("PUT", "/api/v1/connection/"+created.ID.String(), strings.NewReader(`{"layer":"global","properties":{"region":"west"}}`))
		ctx := rpc.ContextWithRequest(context.Background(), request)
		_, err = service.Update(ctx, created.ID.String(), map[string]any{"properties": `{"region":"west"}`})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers.find("orders").Layer.Name).To(Equal("global"))
		Expect(layers.find("orders").Connection.Password).To(Equal("credential-canary"))
		Expect(layers.find("orders").Connection.Properties).To(HaveKeyWithValue("region", "west"))
	})
	It("creates and moves a connection between writable layers while preserving credentials", func() {
		layers := newLayeredStore()
		provider, err := NewStoreProvider(StoreProviderOptions{Store: layers.store, WriteLayer: store.LayerApp, WritableLayers: []string{store.LayerGlobal, store.LayerApp, store.LayerCWD}})
		Expect(err).NotTo(HaveOccurred())
		service := newStoreService(provider, Policy{})
		created, err := service.Create(context.Background(), map[string]any{"name": "orders", "type": "postgres", "url": "postgres://db.example.com/app", "password": "credential-canary", "layer": "global"})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(layers.global, "orders.yaml")).To(BeAnExistingFile())
		moved, err := service.Update(context.Background(), created.ID.String(), map[string]any{"layer": "cwd", "password": ""})
		Expect(err).NotTo(HaveOccurred())
		Expect(moved.ID).To(Equal(created.ID))
		Expect(filepath.Join(layers.global, "orders.yaml")).NotTo(BeAnExistingFile())
		Expect(layers.find("orders").Layer.Name).To(Equal("cwd"))
		Expect(layers.find("orders").Connection.Password).To(Equal("credential-canary"))
	})
	It("serves writable destinations and honest KEK status without any key values", func() {
		layers := newLayeredStore()
		service := newStoreService(newAppStoreProvider(layers, nil), Policy{})
		w := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/connections/storage", nil))
		Expect(w.Code).To(Equal(200))
		var body map[string]any
		Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
		Expect(body).To(HaveKeyWithValue("defaultLayer", "app"))
		Expect(body["kek"]).To(HaveKeyWithValue("state", "not_configured"))
		schema := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/connection", nil)
		r.Header.Set("Accept", "application/schema+json")
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(schema, r)
		Expect(schema.Code).To(Equal(200))
		var form struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		Expect(json.Unmarshal(schema.Body.Bytes(), &form)).To(Succeed())
		// The layer says where a connection is saved, so the form offers it on the
		// save split button rather than as a field.
		Expect(form.Properties["layer"]).To(And(
			HaveKeyWithValue("title", "Save to"),
			HaveKeyWithValue("default", "app"),
			HaveKeyWithValue("x-clicky-submit-target", true),
			HaveKeyWithValue("x-enum-labels", HaveKeyWithValue("app", "Application files")),
		))
	})
})
