package cli

import (
	"context"
	"io"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/connections"
	dbcontext "github.com/flanksource/commons-db/context"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("database connection management", func() {
	It("creates, edits, lists and deletes records through the db source", func() {
		catalogPath := filepath.Join(GinkgoT().TempDir(), "catalog.db")
		database, err := commonsdb.NewGorm("sqlite://"+catalogPath, nil)
		Expect(err).NotTo(HaveOccurred())
		sqlDB, err := database.DB()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(sqlDB.Close)
		Expect(database.Exec(`CREATE TABLE connections (
   id TEXT PRIMARY KEY DEFAULT ('00000000-0000-4000-8000-' || lower(hex(randomblob(6)))), name TEXT, namespace TEXT, source TEXT, type TEXT,
   url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
   insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
  )`).Error).To(Succeed())
		ctx := dbcontext.New().WithDB(database, nil)
		service, err := connections.New(connections.Options{Database: func() (*gorm.DB, error) { return database, nil }, Context: func() dbcontext.Context { return ctx }, DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil }, Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil }})
		Expect(err).NotTo(HaveOccurred())
		run := func(args ...string) error {
			root := New(Options{Open: func(source string) (*connections.Service, func() error, error) {
				Expect(source).To(Equal("db"))
				return service, func() error { return nil }, nil
			}})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append([]string{"--source=db"}, args...))
			return root.Execute()
		}
		Expect(run("add", "sqlite", "--name", "catalog", "--namespace", "test", "--url", "file:original.db")).To(Succeed())
		Expect(run("list")).To(Succeed())
		Expect(run("edit", "connection://test/catalog", "--url", "file:updated.db")).To(Succeed())
		var saved models.Connection
		Expect(database.First(&saved).Error).To(Succeed())
		Expect(saved.URL).To(Equal("file:updated.db"))
		missing := filepath.Join(GinkgoT().TempDir(), "missing.db")
		Expect(run("edit", "catalog", "--url", "file:"+missing)).To(Succeed())
		Expect(run("validate", "catalog")).NotTo(Succeed())
		Expect(missing).NotTo(BeAnExistingFile())
		Expect(run("edit", "catalog", "--url", "file:"+catalogPath)).To(Succeed())
		Expect(run("validate", "catalog")).To(Succeed())
		Expect(run("delete", saved.ID.String())).To(Succeed())
		var count int64
		Expect(database.Model(&models.Connection{}).Count(&count).Error).To(Succeed())
		Expect(count).To(BeZero())
	})
})
