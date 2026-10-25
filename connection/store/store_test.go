package store_test

import (
	gocontext "context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

func TestStore(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "connection/store") }

const header = "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\n"

// write puts a connection file into dir.
func write(dir, file, document string) string {
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	path := filepath.Join(dir, file)
	Expect(os.WriteFile(path, []byte(document), 0o600)).To(Succeed())
	return path
}

var _ = Describe("a layered connection store", func() {
	var global, app, cwd string
	var connections *store.Store
	ctx := dbcontext.NewContext(gocontext.Background())

	BeforeEach(func() {
		root := GinkgoT().TempDir()
		global, app, cwd = filepath.Join(root, "global"), filepath.Join(root, "app"), filepath.Join(root, "cwd")
		var err error
		connections, err = store.New(
			store.Layer{Name: store.LayerGlobal, Dir: global},
			store.Layer{Name: store.LayerApp, Dir: app},
			store.Layer{Name: store.LayerCWD, Dir: cwd},
		)
		Expect(err).ToNot(HaveOccurred())
	})

	resolve := func(reference string) *models.Connection {
		connection, err := connections.ResolveConnection(ctx, reference)
		Expect(err).ToNot(HaveOccurred())
		return connection
	}

	It("reads a typed connection as the connection mission-control persists", func() {
		path := write(app, "orders.yaml", header+"metadata: {name: orders, namespace: shop}\nspec:\n  mssql:\n    url: {value: 'sqlserver://db:1433'}\n    password: {valueFrom: {secretKeyRef: {name: orders, key: password}}}\n")
		connection := resolve("connection://shop/orders")
		Expect(connection).ToNot(BeNil())
		Expect(*connection).To(MatchFields(IgnoreExtras, Fields{
			"Name": Equal("orders"), "Namespace": Equal("shop"), "Type": Equal(models.ConnectionTypeSQLServer),
			"URL": Equal("sqlserver://db:1433"), "Password": Equal("secret://orders/password"),
			"Source": Equal("file://" + path), "Virtual": BeTrue(),
		}))
	})

	It("lets a later layer override an earlier one's connection of the same name", func() {
		write(global, "logs.yaml", header+"spec: {loki: {url: 'http://global'}}\n")
		write(app, "logs.yaml", header+"spec: {loki: {url: 'http://app'}}\n")
		write(cwd, "logs.yaml", header+"spec: {loki: {url: 'http://cwd'}}\n")
		write(global, "only-global.yaml", header+"spec: {loki: {url: 'http://only'}}\n")

		Expect(resolve("logs").URL).To(Equal("http://cwd"))
		listed, err := connections.ListConnections()
		Expect(err).ToNot(HaveOccurred())
		Expect(listed).To(HaveLen(2))
		Expect(listed[0].Name).To(Equal("logs"))
		Expect(listed[0].Source).To(Equal("file://" + filepath.Join(cwd, "logs.yaml")))
		Expect(listed[1].Name).To(Equal("only-global"))
	})

	DescribeTable("resolves every form of reference to a connection",
		func(reference string) {
			write(app, "orders.yaml", header+"metadata: {name: orders, namespace: shop}\nspec: {postgres: {url: {value: 'postgres://orders'}}}\n")
			Expect(resolve(reference)).ToNot(BeNil())
		},
		Entry("a bare name", "orders"),
		Entry("connection://name", "connection://orders"),
		Entry("connection://namespace/name", "connection://shop/orders"),
		Entry("the deprecated connection://type/namespace/name", "connection://postgres/shop/orders"),
	)

	It("resolves a connection by the stable id it gives it", func() {
		write(app, "orders.yaml", header+"spec: {postgres: {url: {value: 'postgres://orders'}}}\n")
		id := resolve("orders").ID
		Expect(resolve(id.String()).Name).To(Equal("orders"))
		Expect(resolve("orders").ID).To(Equal(id))
	})

	It("answers nil for a reference it does not define, and for another namespace", func() {
		write(app, "orders.yaml", header+"metadata: {namespace: shop}\nspec: {postgres: {}}\n")
		Expect(resolve("connection://missing")).To(BeNil())
		Expect(resolve("connection://other/orders")).To(BeNil())
		Expect(resolve("postgres://inline")).To(BeNil())
	})

	It("follows a file's edits: its modification time is the connection's", func() {
		path := write(app, "orders.yaml", header+"spec: {postgres: {}}\n")
		later := time.Now().Add(time.Hour).Truncate(time.Second)
		Expect(os.Chtimes(path, later, later)).To(Succeed())
		Expect(resolve("orders").UpdatedAt).To(BeTemporally("==", later))
	})

	DescribeTable("refuses a layer it cannot read, naming the file",
		func(files map[string]string, expected string) {
			for file, document := range files {
				write(app, file, document)
			}
			_, err := connections.ListConnections()
			Expect(err).To(MatchError(And(ContainSubstring(app), ContainSubstring(expected))))
		},
		Entry("a file that is not YAML", map[string]string{"bad.yaml": "spec: [unclosed"}, "decode connection"),
		Entry("an unknown field", map[string]string{"bad.yaml": header + "spec: {postgres: {colour: red}}\n"}, "unknown field"),
		Entry("another kind", map[string]string{"bad.yaml": "kind: Playbook\nspec: {postgres: {}}\n"}, `kind is "Playbook"`),
		Entry("no kind of connection", map[string]string{"bad.yaml": header + "spec: {}\n"}, "names no kind of connection"),
		Entry("one connection in two files", map[string]string{
			"a.yaml": header + "metadata: {name: dup}\nspec: {postgres: {}}\n",
			"b.yaml": header + "metadata: {name: dup}\nspec: {loki: {url: x}}\n",
		}, "both define connection /dup"),
	)

	It("skips layers that do not exist and files that are not connections", func() {
		write(app, "README.md", "# not a connection")
		write(app, ".hidden.yaml", "not: read")
		Expect(connections.ListConnections()).To(BeEmpty())
	})

	It("refuses a name two namespaces define, unless the reference names one", func() {
		write(app, "a.yaml", header+"metadata: {name: db, namespace: a}\nspec: {postgres: {}}\n")
		write(app, "b.yaml", header+"metadata: {name: db, namespace: b}\nspec: {postgres: {}}\n")
		_, err := connections.ResolveConnection(ctx, "db")
		Expect(err).To(MatchError(ContainSubstring("ambiguous")))
		Expect(resolve("connection://b/db").Namespace).To(Equal("b"))
	})

	It("saves a typed connection into a layer, readable by its owner alone, and deletes it", func() {
		resource := &crd.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "logs"},
			Spec:       crd.ConnectionSpec{Loki: &crd.ConnectionLoki{URL: "http://loki", Password: types.EnvVar{ValueStatic: "secret://loki/password"}}},
		}
		Expect(connections.Save(store.LayerCWD, resource)).To(Succeed())
		info, err := os.Stat(filepath.Join(cwd, "logs.yaml"))
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(resolve("logs")).To(PointTo(MatchFields(IgnoreExtras, Fields{"Type": Equal(models.ConnectionTypeLoki), "Password": Equal("secret://loki/password")})))

		resource.Spec.Loki.URL = "http://loki-2"
		Expect(connections.Save(store.LayerCWD, resource)).To(Succeed())
		Expect(resolve("logs").URL).To(Equal("http://loki-2"))

		Expect(connections.Delete(store.LayerCWD, "logs")).To(Succeed())
		Expect(resolve("logs")).To(BeNil())
		Expect(connections.Delete(store.LayerCWD, "logs")).To(MatchError(os.ErrNotExist))
	})

	It("refuses to save into a layer it does not have, or a connection that names no kind", func() {
		resource := &crd.Connection{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
		Expect(connections.Save("nowhere", resource)).To(MatchError(ContainSubstring(`no layer "nowhere"`)))
		Expect(connections.Save(store.LayerApp, resource)).To(MatchError(ContainSubstring("names no kind of connection")))
	})

	It("answers a context's connection references, hydrated, before the resolver it had", func() {
		write(app, "orders.yaml", header+"spec:\n  postgres:\n    host: {value: 'db:5432'}\n    database: {value: orders}\n    username: {value: app}\n    password: {value: s3cret}\n")
		previous := &models.Connection{Name: "elsewhere", Type: models.ConnectionTypeLoki}
		attached := connections.Attach(ctx.WithConnectionResolver(func(_ dbcontext.Context, reference string) (*models.Connection, error) {
			if reference == "connection://elsewhere" {
				return previous, nil
			}
			return nil, nil
		}))

		connection, err := dbcontext.HydrateConnectionByURL(attached, "connection://orders")
		Expect(err).ToNot(HaveOccurred())
		Expect(connection.URL).To(Equal("postgres://app:s3cret@db:5432/orders"))
		connection, err = dbcontext.HydrateConnectionByURL(attached, "orders")
		Expect(err).ToNot(HaveOccurred())
		Expect(connection.Type).To(Equal(models.ConnectionTypePostgres))
		connection, err = dbcontext.HydrateConnectionByURL(attached, "connection://elsewhere")
		Expect(err).ToNot(HaveOccurred())
		Expect(connection.Type).To(Equal(models.ConnectionTypeLoki))
		_, err = dbcontext.HydrateConnectionByURL(attached, "connection://missing")
		Expect(err).To(MatchError(ContainSubstring("db is not configured")))
	})
})

var _ = Describe("the default layers", func() {
	It("are the shared connections, the app's, then the working directory's", func() {
		root := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", root)
		cwd, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(store.DefaultLayers("telltale")).To(Equal([]store.Layer{
			{Name: store.LayerGlobal, Dir: filepath.Join(root, "flanksource", "connections")},
			{Name: store.LayerApp, Dir: filepath.Join(root, "telltale", "connections")},
			{Name: store.LayerCWD, Dir: filepath.Join(cwd, ".connections")},
		}))
	})

	It("need an app of their own", func() {
		_, err := store.DefaultLayers(store.GlobalApp)
		Expect(err).To(MatchError(ContainSubstring("an app name other than")))
	})
})
