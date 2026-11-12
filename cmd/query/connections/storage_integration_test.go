package connections

import (
	"context"
	"path/filepath"

	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("database and file storage moves", func() {
	It("moves credentials from a file into the database and back without keeping the old entry", func() {
		db := openPostgresConflictDB()
		createReferencedConnectionTables(db, "RESTRICT")
		Expect(db.Exec("ALTER TABLE connections ALTER COLUMN id SET DEFAULT gen_random_uuid()::text").Error).To(Succeed())
		layers := newLayeredStore()
		provider, err := NewStoreProvider(StoreProviderOptions{Store: layers.store, WriteLayer: store.LayerApp, WritableLayers: []string{store.LayerApp, store.LayerGlobal, store.LayerCWD}})
		Expect(err).NotTo(HaveOccurred())
		service := newConflictTestService(db)
		service.SetVirtual(provider)
		file, err := service.Create(context.Background(), map[string]any{"name": "orders", "type": "postgres", "url": "postgres://db.example.com/app", "password": "credential-canary", "layer": "app"})
		Expect(err).NotTo(HaveOccurred())
		database, err := service.Update(context.Background(), file.ID.String(), map[string]any{"layer": "db", "password": ""})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(layers.app, "orders.yaml")).NotTo(BeAnExistingFile())
		Expect(database.ID).To(Equal(file.ID))
		var persisted models.Connection
		Expect(db.First(&persisted, "id = ?", database.ID).Error).To(Succeed())
		Expect(persisted.Password).To(Equal("credential-canary"))
		Expect(db.Exec("INSERT INTO database_modules (id, connection_id) VALUES ('dependent', ?)", database.ID).Error).To(Succeed())
		_, err = service.Update(context.Background(), database.ID.String(), map[string]any{"layer": "global"})
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(layers.global, "orders.yaml")).NotTo(BeAnExistingFile())
		Expect(db.First(&persisted, "id = ?", database.ID).Error).To(Succeed())
		Expect(db.Exec("DELETE FROM database_modules WHERE id = 'dependent'").Error).To(Succeed())
		moved, err := service.Update(context.Background(), database.ID.String(), map[string]any{"layer": "global", "password": ""})
		Expect(err).NotTo(HaveOccurred())
		Expect(moved.ID).To(Equal(database.ID))
		Expect(layers.find("orders").Layer.Name).To(Equal("global"))
		Expect(layers.find("orders").Connection.Password).To(Equal("credential-canary"))
		var count int64
		Expect(db.Model(&models.Connection{}).Count(&count).Error).To(Succeed())
		Expect(count).To(BeZero())
	})
})
