package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/cmd/query/snapshots"
	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Folder connections", func() {
	It("uses XDG and app folders with live overrides", func() {
		root := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", root)
		config := filepath.Join(root, "query-state")
		global := filepath.Join(root, "flanksource", "connections")
		local := filepath.Join(config, "connections")
		for _, dir := range []string{global, local} {
			Expect(os.MkdirAll(dir, 0700)).To(Succeed())
		}
		manifest := "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nmetadata:\n  name: local-picker-example\nspec:\n  type: sqlite\n  url: {value: 'file:/global.db'}\n"
		Expect(os.WriteFile(filepath.Join(global, "example.yaml"), []byte(manifest), 0600)).To(Succeed())
		manager, err := snapshots.New(snapshots.Options{Dir: filepath.Join(root, "snapshots"), MaxAge: time.Hour})
		Expect(err).NotTo(HaveOccurred())
		catalog, err := newConnectionCatalog(config, manager)
		Expect(err).NotTo(HaveOccurred())
		connection, err := catalog.ResolveConnection(dbcontext.New(), "connection://local-picker-example")
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Warnings).To(BeEmpty())
		Expect(connection.URL).To(Equal("file:/global.db"))
		Expect(connection.ReadOnly).To(BeTrue())
		Expect(connection.Virtual).To(BeTrue())
		override := "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\nmetadata:\n  name: local-picker-example\nspec:\n  type: sqlite\n  url: {value: 'file:/app.db'}\n"
		Expect(os.WriteFile(filepath.Join(local, "example.yaml"), []byte(override), 0600)).To(Succeed())
		connection, err = catalog.ResolveConnection(dbcontext.New(), "connection://local-picker-example")
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.URL).To(Equal("file:/app.db"))
		Expect(connection.Source).To(Equal("file://" + filepath.Join(local, "example.yaml")))
	})
})
