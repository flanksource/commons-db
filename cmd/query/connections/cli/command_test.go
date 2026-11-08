package cli

import (
	"context"
	"github.com/flanksource/clicky/prompt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCLI(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Connection CLI") }

var _ = Describe("connection management", func() {
	var files *store.Store
	var run func(...string) error
	var commandContext context.Context
	BeforeEach(func() {
		commandContext = context.Background()
		base := GinkgoT().TempDir()
		var err error
		files, err = store.New(store.Layer{Name: "global", Dir: filepath.Join(base, "global")}, store.Layer{Name: "app", Dir: filepath.Join(base, "app")}, store.Layer{Name: "cwd", Dir: filepath.Join(base, "cwd")})
		Expect(err).NotTo(HaveOccurred())
		provider, err := connections.NewStoreProvider(connections.StoreProviderOptions{Store: files, WriteLayer: "global", WritableLayers: []string{"global", "app", "cwd"}})
		Expect(err).NotTo(HaveOccurred())
		ctx := files.Attach(dbcontext.New())
		service, err := connections.New(connections.Options{Virtual: provider, Context: func() dbcontext.Context { return ctx }, DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil }, Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil }})
		Expect(err).NotTo(HaveOccurred())
		run = func(args ...string) error {
			root := New(Options{Open: func(source string) (*connections.Service, func() error, error) {
				Expect(source).To(Equal("file"))
				return service, func() error { return nil }, nil
			}})
			root.SetOut(io.Discard)
			root.SetContext(commandContext)
			root.SetErr(io.Discard)
			root.SetArgs(args)
			return root.Execute()
		}
	})
	It("prefills Clicky edit forms and cancels without writing", func() {
		Expect(run("add", "sqlite", "--name", "interactive", "--url", "file:original.db")).To(Succeed())
		manager := prompt.NewManager(prompt.NewMemory(prompt.MemoryConfig{}))
		previous := prompt.GlobalManager()
		prompt.SetDefault(manager)
		DeferCleanup(func() { prompt.SetDefault(previous) })
		commandContext = prompt.WithScope(context.Background(), prompt.Scope{Owner: "connection-edit"})
		done := make(chan error, 1)
		go func() { defer GinkgoRecover(); done <- run("edit", "interactive", "-i") }()
		var snapshot prompt.PromptSnapshot
		Eventually(func() int {
			pending := manager.List(prompt.Filter{Owner: "connection-edit", State: "pending"})
			if len(pending) > 0 {
				snapshot = pending[0]
			}
			return len(pending)
		}, time.Second*5, time.Millisecond*10).Should(Equal(1))
		Expect(snapshot.Value["url"]).To(Equal("file:original.db"))
		Expect(manager.Resolve(snapshot.ID, prompt.Answer{Cancelled: true})).To(Succeed())
		Eventually(done).Should(Receive(HaveOccurred()))
		entry, err := files.Find("interactive")
		Expect(err).NotTo(HaveOccurred())
		Expect(entry.Connection.URL).To(Equal("file:original.db"))
	})
	It("edits owning files and preserves credentials, properties and explicit false", func() {
		for _, layer := range []string{"global", "app", "cwd"} {
			resource, err := crd.FromModel(models.Connection{Name: layer, Type: "http", URL: "http://localhost", Password: "secret://credentials/password", InsecureTLS: true, Properties: map[string]string{"keep": "yes"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(files.Save(layer, resource)).To(Succeed())
			Expect(run("edit", layer, "--url", "http://127.0.0.1", "--insecure-tls=false")).To(Succeed())
			entry, err := files.Find(layer)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry.Layer.Name).To(Equal(layer))
			Expect(entry.Connection.Password).To(Equal("secret://credentials/password"))
			Expect(entry.Connection.Properties["keep"]).To(Equal("yes"))
			Expect(entry.Connection.InsecureTLS).To(BeFalse())
			info, err := os.Stat(entry.Path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))
			Expect(run("delete", layer)).To(Succeed())
			entry, err = files.Find(layer)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry).To(BeNil())
		}
	})
	It("validates authenticated HTTP without saving hydrated credentials", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "tester" || password != "example-value" {
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()
		Expect(run("add", "http", "--name", "web", "--url", server.URL, "--username", "tester", "--password", "example-value")).To(Succeed())
		Expect(run("validate", "web")).To(Succeed())
		Expect(run("edit", "web", "--password", "wrong")).To(Succeed())
		Expect(run("validate", "web")).NotTo(Succeed())
	})
	It("refuses an ambiguous name and accepts a qualified reference", func() {
		for _, namespace := range []string{"one", "two"} {
			resource, err := crd.FromModel(models.Connection{Name: "shared", Namespace: namespace, Type: "sqlite", URL: "file:example.db"})
			Expect(err).NotTo(HaveOccurred())
			Expect(files.Save(namespaceLayer(namespace), resource)).To(Succeed())
		}
		Expect(run("delete", "shared")).NotTo(Succeed())
		Expect(run("delete", "connection://one/shared")).To(Succeed())
	})
	It("redacts URL credentials, DSNs and sensitive properties", func() {
		output := map[string]any{"url": "https://tester:example@localhost?token=example", "properties": map[string]any{"clientSecret": "example", "region": "local"}}
		redact(output)
		Expect(output["url"]).To(Equal("https://localhost?token=redacted"))
		Expect(output["properties"]).To(Equal(map[string]any{"clientSecret": "", "region": "local"}))
		Expect(redactURL("server=localhost;password=example")).To(Equal("server=localhost;password=redacted"))
	})
})

func namespaceLayer(namespace string) string {
	if namespace == "one" {
		return "global"
	}
	return "app"
}
