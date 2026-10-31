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

	DescribeTable("lists a file it cannot read as a warning on that connection alone, naming the file",
		func(file, document, name, expected string) {
			path := write(app, file, document)
			write(app, "good.yaml", header+"spec: {postgres: {url: {value: 'postgres://good'}}}\n")
			listed, err := connections.ListConnections()
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(2))
			byName := map[string]*models.Connection{}
			for _, connection := range listed {
				byName[connection.Name] = connection
			}
			Expect(byName["good"].Warnings).To(BeEmpty())
			Expect(byName[name]).ToNot(BeNil(), "the broken file is listed under %q", name)
			Expect(byName[name].Source).To(Equal("file://" + path))
			Expect(byName[name].Warnings).To(ConsistOf(And(ContainSubstring(path), ContainSubstring(expected))))
			Expect(resolve("good").URL).To(Equal("postgres://good"))
			_, err = dbcontext.HydrateConnectionByURL(connections.Attach(ctx), "connection://"+name)
			Expect(err).To(MatchError(And(ContainSubstring("cannot be used"), ContainSubstring(path), ContainSubstring(expected))))
		},
		Entry("a file that is not YAML", "bad.yaml", "spec: [unclosed", "bad", "decode connection"),
		Entry("an unknown field", "bad.yaml", header+"spec: {postgres: {colour: red}}\n", "bad", "unknown field"),
		Entry("another kind", "bad.yaml", "apiVersion: mission-control.flanksource.com/v1\nkind: Playbook\nspec: {postgres: {}}\n", "bad", `kind is "Playbook"`),
		Entry("a legacy flat connection, with no apiVersion or kind", "legacy.yaml",
			"name: orders\ntype: postgres\nurl: postgres://db/orders\npassword: secret://orders/password\n", "legacy",
			"legacy.yaml is not a Connection resource: it needs apiVersion: "+crd.APIVersion+" and kind: Connection"),
		Entry("a resource with a kind but no apiVersion", "bad.yaml", "kind: Connection\nspec: {postgres: {}}\n", "bad", "needs apiVersion: "+crd.APIVersion),
		Entry("no kind of connection, named by its metadata", "bad.yaml", header+"metadata: {name: named}\nspec: {}\n", "named", "names no kind of connection"),
	)

	It("keeps the first of two files defining one connection and warns about the other", func() {
		write(app, "a.yaml", header+"metadata: {name: dup}\nspec: {postgres: {url: {value: 'postgres://first'}}}\n")
		second := write(app, "b.yaml", header+"metadata: {name: dup}\nspec: {loki: {url: x}}\n")
		listed, err := connections.ListConnections()
		Expect(err).ToNot(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].URL).To(Equal("postgres://first"))
		Expect(listed[0].Warnings).To(ConsistOf(ContainSubstring(second + " also defines connection /dup")))
	})

	It("finds the layer and file that define the connection in effect", func() {
		write(global, "logs.yaml", header+"spec: {loki: {url: 'http://global'}}\n")
		path := write(app, "logs.yaml", header+"spec: {loki: {url: 'http://app'}}\n")
		found, err := connections.Find("connection://logs")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(PointTo(MatchFields(IgnoreExtras, Fields{
			"Layer":      Equal(store.Layer{Name: store.LayerApp, Dir: app}),
			"Path":       Equal(path),
			"Connection": MatchFields(IgnoreExtras, Fields{"Name": Equal("logs"), "URL": Equal("http://app")}),
		})))
		Expect(connections.Find(found.Connection.ID.String())).To(PointTo(HaveField("Path", path)))

		missing, err := connections.Find("connection://missing")
		Expect(err).ToNot(HaveOccurred())
		Expect(missing).To(BeNil())
	})

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

	It("prefers the connection of the namespace a reference names over one with no namespace, resolving and saving", func() {
		write(app, "orders.yaml", header+"metadata: {name: orders}\nspec: {loki: {url: 'http://any'}}\n")
		shop := write(app, "shop-orders.yaml", header+"metadata: {name: orders, namespace: shop}\nspec: {loki: {url: 'http://shop'}}\n")
		Expect(resolve("connection://shop/orders").URL).To(Equal("http://shop"))
		Expect(resolve("connection://other/orders").URL).To(Equal("http://any"))

		resource := &crd.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "shop"},
			Spec:       crd.ConnectionSpec{Loki: &crd.ConnectionLoki{URL: "http://shop-2"}},
		}
		Expect(connections.Save(store.LayerApp, resource)).To(Succeed())
		Expect(resolve("connection://shop/orders").Source).To(Equal("file://" + shop))
		Expect(resolve("connection://shop/orders").URL).To(Equal("http://shop-2"))
	})

	DescribeTable("refuses to save a connection whose name is not a plain file name",
		func(name string) {
			resource := &crd.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: crd.ConnectionSpec{Loki: &crd.ConnectionLoki{URL: "http://loki"}}}
			Expect(connections.Save(store.LayerCWD, resource)).To(MatchError(ContainSubstring("must be a plain file name")))
			Expect(filepath.Join(cwd, "..", "escaped.yaml")).ToNot(BeAnExistingFile())
		},
		Entry("one that climbs out of the layer", "../escaped"),
		Entry("one in a subdirectory", "nested/logs"),
		Entry("a hidden one the layer would skip", ".logs"),
	)

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
