package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const connectionHeader = "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\n"

// layeredStore is a connection store over global, app and cwd layers under a
// fresh temp dir.
type layeredStore struct {
	global, app, cwd string
	store            *store.Store
}

func newLayeredStore() layeredStore {
	root := GinkgoT().TempDir()
	layers := layeredStore{global: filepath.Join(root, "global"), app: filepath.Join(root, "app"), cwd: filepath.Join(root, "cwd")}
	var err error
	layers.store, err = store.New(
		store.Layer{Name: store.LayerGlobal, Dir: layers.global},
		store.Layer{Name: store.LayerApp, Dir: layers.app},
		store.Layer{Name: store.LayerCWD, Dir: layers.cwd},
	)
	Expect(err).NotTo(HaveOccurred())
	return layers
}

func writeConnectionFile(dir, file, document string) string {
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	path := filepath.Join(dir, file)
	Expect(os.WriteFile(path, []byte(connectionHeader+document), 0o600)).To(Succeed())
	return path
}

func (l layeredStore) find(reference string) *store.Entry {
	entry, err := l.store.Find(reference)
	Expect(err).NotTo(HaveOccurred())
	return entry
}

func newStoreService(provider *StoreProvider, policy Policy) *Service {
	service, err := New(Options{
		Context:    func() dbcontext.Context { return dbcontext.NewContext(context.Background()) },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
		Virtual:    provider,
		Policy:     policy,
	})
	Expect(err).NotTo(HaveOccurred())
	return service
}

func newAppStoreProvider(layers layeredStore, validateDelete func(context.Context, models.Connection) error) *StoreProvider {
	provider, err := NewStoreProvider(StoreProviderOptions{Store: layers.store, WriteLayer: store.LayerApp, ValidateDelete: validateDelete})
	Expect(err).NotTo(HaveOccurred())
	return provider
}

func expectStatus(err error, status int, code string) {
	var statusErr *entity.StatusError
	Expect(errors.As(err, &statusErr)).To(BeTrue(), "expected a StatusError, got %v", err)
	Expect([]any{statusErr.Status, statusErr.Code}).To(Equal([]any{status, code}), "%v", err)
}

var _ = Describe("connections stored as layered files, through the service", func() {
	var (
		layers  layeredStore
		service *Service
		entraID string
	)
	BeforeEach(func() {
		layers = newLayeredStore()
		writeConnectionFile(layers.app, "entra.yaml", "metadata: {name: entra}\nspec:\n  type: azure\n  username: {value: client-before}\n"+
			"  password: {valueFrom: {secretKeyRef: {name: tenant, key: client-secret}}}\n  certificate: {value: certificate-before}\n  properties: {tenant: tenant-x}\n")
		writeConnectionFile(layers.global, "shared.yaml", "spec: {loki: {url: 'http://loki'}}\n")
		service = newStoreService(newAppStoreProvider(layers, nil), Policy{})
		entraID = layers.find("entra").Connection.ID.String()
	})

	It("advertises the app layer's connections as editable and every other layer's as read-only", func() {
		entra, err := service.Get(entraID)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{entra.Virtual, entra.ReadOnly, entra.Username, entra.Password, entra.Certificate}).
			To(Equal([]any{false, false, "client-before", "secret://tenant/client-secret", ""}))

		shared, err := service.Get("shared")
		Expect(err).NotTo(HaveOccurred())
		Expect(shared.ReadOnly).To(BeTrue())

		recorder := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/connections/dashboard", nil))
		Expect(recorder.Code).To(Equal(http.StatusOK))
		var dashboard struct {
			ReadOnly bool `json:"readOnly"`
		}
		Expect(json.Unmarshal(recorder.Body.Bytes(), &dashboard)).To(Succeed())
		Expect(dashboard.ReadOnly).To(BeFalse())
	})

	It("lists a broken connection file as a warning on its own row, under a type policy too, and lets it be deleted", func() {
		legacy := filepath.Join(layers.app, "legacy.yaml")
		Expect(os.WriteFile(legacy, []byte("type: postgres\nurl: postgres://db/orders\n"), 0o600)).To(Succeed())
		service = newStoreService(newAppStoreProvider(layers, nil), Policy{AllowedTypes: []string{"azure"}})

		recorder := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/connections/dashboard", nil))
		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		var dashboard struct {
			Connections []struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				Warnings []string `json:"warnings"`
			} `json:"connections"`
		}
		Expect(json.Unmarshal(recorder.Body.Bytes(), &dashboard)).To(Succeed())
		warnings := map[string][]string{}
		var legacyID string
		for _, row := range dashboard.Connections {
			warnings[row.Name] = row.Warnings
			if row.Name == "legacy" {
				legacyID = row.ID
			}
		}
		Expect(warnings).To(HaveKeyWithValue("entra", BeEmpty()))
		Expect(warnings).To(HaveKeyWithValue("legacy", ConsistOf(ContainSubstring(legacy+" is not a Connection resource"))))

		Expect(service.Delete(legacyID)).To(Succeed())
		Expect(legacy).NotTo(BeAnExistingFile())
	})

	It("saves an edit into the connection's file, keeping credentials left blank", func() {
		updated, err := service.Update(context.Background(), entraID, map[string]any{
			"name": "entra", "type": "azure", "username": "client-after", "password": "", "certificate": "",
			"properties": map[string]any{"tenant": "tenant-y"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.ID.String()).To(Equal(entraID))
		Expect(updated.Password).To(Equal("secret://tenant/client-secret"))

		stored := layers.find("entra")
		Expect(stored.Path).To(Equal(filepath.Join(layers.app, "entra.yaml")))
		Expect([]any{stored.Connection.Username, stored.Connection.Password, stored.Connection.Certificate, stored.Connection.Properties}).
			To(Equal([]any{"client-after", "secret://tenant/client-secret", "certificate-before", types.JSONStringMap{"tenant": "tenant-y"}}))
	})

	It("creates a typed connection file in the app layer and deletes it, without a database", func() {
		created, err := service.Create(context.Background(), map[string]any{"name": "archive", "type": models.ConnectionTypeSQLite, "url": "file:/data/archive.db"})
		Expect(err).NotTo(HaveOccurred())
		path := filepath.Join(layers.app, "archive.yaml")
		Expect(os.ReadFile(path)).To(ContainSubstring("kind: Connection"))
		Expect(created.ID).To(Equal(layers.find("archive").Connection.ID))

		Expect(service.Delete(created.ID.String())).To(Succeed())
		Expect(path).NotTo(BeAnExistingFile())
	})

	DescribeTable("round trips credential sources without resolving them",
		func(reference, visible string) {
			updated, err := service.Update(context.Background(), entraID, map[string]any{
				"name": "entra", "type": "azure", "namespace": "tenant-x", "username": "client-before", "password": reference,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Password).To(Equal(visible))
			stored := layers.find("connection://tenant-x/entra")
			Expect([]any{stored.Connection.Password, stored.Connection.Namespace}).To(Equal([]any{reference, "tenant-x"}))
		},
		Entry("Kubernetes Secret", "secret://credentials/password", "secret://credentials/password"),
		Entry("ConfigMap", "configmap://credentials/password", "configmap://credentials/password"),
		Entry("Helm", "helm://application/credentials.password", "helm://application/credentials.password"),
		Entry("service account", "serviceaccount://reader", "serviceaccount://reader"),
		Entry("1Password", "op://vault/item/password", "op://vault/item/password"),
		Entry("literal", "inline-credential", ""),
	)

	It("refuses edits to another layer's connection", func() {
		_, err := service.Update(context.Background(), "shared", map[string]any{"name": "shared", "type": models.ConnectionTypeLoki})
		expectStatus(err, http.StatusMethodNotAllowed, "read_only")
		expectStatus(service.Delete("shared"), http.StatusMethodNotAllowed, "read_only")
	})

	It("refuses edits and advertises read-only once the app layer's directory is read-only", func() {
		before, err := os.ReadFile(filepath.Join(layers.app, "entra.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chmod(layers.app, 0o500)).To(Succeed())
		DeferCleanup(func() { Expect(os.Chmod(layers.app, 0o700)).To(Succeed()) })

		entra, err := service.Get(entraID)
		Expect(err).NotTo(HaveOccurred())
		Expect(entra.ReadOnly).To(BeTrue())
		_, err = service.Update(context.Background(), entraID, map[string]any{"name": "entra", "type": "azure"})
		expectStatus(err, http.StatusMethodNotAllowed, "read_only")
		Expect(os.ReadFile(filepath.Join(layers.app, "entra.yaml"))).To(Equal(before))
	})
})

var _ = Describe("the store provider", func() {
	const ordersURL = "postgres://db.example/orders"
	var (
		layers   layeredStore
		provider *StoreProvider
		ctx      context.Context
	)
	BeforeEach(func() {
		layers = newLayeredStore()
		provider = newAppStoreProvider(layers, nil)
		ctx = context.Background()
	})
	orders := func() *models.Connection {
		return &models.Connection{Name: "orders", Type: models.ConnectionTypePostgres, URL: ordersURL}
	}

	It("saves, resolves, renames and deletes a connection through its file", func() {
		saved, err := provider.SaveConnection(ctx, orders())
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.ID).To(Equal(layers.find("orders").Connection.ID))
		for _, reference := range []string{"orders", "connection://orders", saved.ID.String()} {
			resolved, err := provider.ResolveConnection(dbcontext.NewContext(ctx), reference)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).NotTo(BeNil(), reference)
			Expect([]any{resolved.Name, resolved.URL, resolved.Virtual}).To(Equal([]any{"orders", ordersURL, false}))
		}

		renamed, err := provider.SaveConnection(ctx, &models.Connection{ID: saved.ID, Name: "billing", Type: models.ConnectionTypePostgres, URL: ordersURL})
		Expect(err).NotTo(HaveOccurred())
		Expect(renamed.Name).To(Equal("billing"))
		Expect(filepath.Join(layers.app, "billing.yaml")).To(BeAnExistingFile())
		Expect(filepath.Join(layers.app, "orders.yaml")).NotTo(BeAnExistingFile())

		Expect(provider.DeleteConnection(ctx, renamed.ID.String())).To(Succeed())
		Expect(provider.ListConnections()).To(BeEmpty())
	})

	DescribeTable("refuses saves the store cannot hold",
		func(connection models.Connection, status int, code string) {
			writeConnectionFile(layers.global, "shared.yaml", "spec: {loki: {url: 'http://loki'}}\n")
			_, err := provider.SaveConnection(ctx, orders())
			Expect(err).NotTo(HaveOccurred())
			_, err = provider.SaveConnection(ctx, &connection)
			expectStatus(err, status, code)
		},
		Entry("a new connection reusing a name", models.Connection{Name: "orders", Type: models.ConnectionTypePostgres, URL: ordersURL}, http.StatusConflict, "already_exists"),
		Entry("a name another layer defines", models.Connection{Name: "shared", Type: models.ConnectionTypeLoki, URL: "http://other"}, http.StatusConflict, "already_exists"),
		Entry("an update of an unknown id", models.Connection{ID: uuid.New(), Name: "orders", Type: models.ConnectionTypePostgres}, http.StatusNotFound, "not_found"),
		Entry("a name with a path separator", models.Connection{Name: "../orders", Type: models.ConnectionTypePostgres}, http.StatusBadRequest, "invalid_name"),
	)

	It("refuses to rename onto another connection's name", func() {
		saved, err := provider.SaveConnection(ctx, orders())
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.SaveConnection(ctx, &models.Connection{Name: "billing", Type: models.ConnectionTypePostgres})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.SaveConnection(ctx, &models.Connection{ID: saved.ID, Name: "billing", Type: models.ConnectionTypePostgres})
		expectStatus(err, http.StatusConflict, "already_exists")
		Expect(filepath.Join(layers.app, "orders.yaml")).To(BeAnExistingFile())
	})

	It("lets the host veto a delete, and reports an unknown one as not found", func() {
		vetoed := newAppStoreProvider(layers, func(_ context.Context, connection models.Connection) error {
			return entity.NewStatusErrorf(http.StatusConflict, "connection_in_use", "ai.connection requires connection %q", connection.Name)
		})
		saved, err := vetoed.SaveConnection(ctx, orders())
		Expect(err).NotTo(HaveOccurred())
		expectStatus(vetoed.DeleteConnection(ctx, saved.ID.String()), http.StatusConflict, "connection_in_use")
		Expect(filepath.Join(layers.app, "orders.yaml")).To(BeAnExistingFile())
		expectStatus(vetoed.DeleteConnection(ctx, "missing"), http.StatusNotFound, "not_found")
	})

	It("lists each name from the last layer that defines it, editable only in the app layer", func() {
		writeConnectionFile(layers.global, "logs.yaml", "spec: {loki: {url: 'http://global'}}\n")
		writeConnectionFile(layers.app, "logs.yaml", "spec: {loki: {url: 'http://app'}}\n")
		writeConnectionFile(layers.cwd, "logs.yaml", "spec: {loki: {url: 'http://cwd'}}\n")
		writeConnectionFile(layers.app, "orders.yaml", "spec: {postgres: {url: {value: 'postgres://app'}}}\n")
		writeConnectionFile(layers.global, "shared.yaml", "spec: {loki: {url: 'http://shared'}}\n")

		listed, err := provider.ListConnections()
		Expect(err).NotTo(HaveOccurred())
		view := map[string][]any{}
		for _, connection := range listed {
			view[connection.Name] = []any{connection.URL, connection.Virtual, connection.ReadOnly}
		}
		Expect(view).To(Equal(map[string][]any{
			"logs":   {"http://cwd", true, true},
			"orders": {"postgres://app", false, false},
			"shared": {"http://shared", true, true},
		}))
	})

	It("needs a store and one of its layers to write to", func() {
		_, err := NewStoreProvider(StoreProviderOptions{WriteLayer: store.LayerApp})
		Expect(err).To(MatchError(ContainSubstring("store is required")))
		_, err = NewStoreProvider(StoreProviderOptions{Store: layers.store, WriteLayer: "nowhere"})
		Expect(err).To(MatchError(ContainSubstring(`no layer "nowhere"`)))
	})
})
