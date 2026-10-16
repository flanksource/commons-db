package connections

import (
	"context"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// snapshotProvider serves one virtual, read-only connection the way the query
// app's snapshot manager does.
type snapshotProvider struct{ snapshot *models.Connection }

func (p snapshotProvider) ListConnections() []*models.Connection {
	return []*models.Connection{p.snapshot}
}

func (p snapshotProvider) ResolveConnection(_ dbcontext.Context, id string) (*models.Connection, error) {
	if id == p.snapshot.Name || id == p.snapshot.ID.String() {
		return p.snapshot, nil
	}
	return nil, nil
}

var _ = Describe("sqlite connections", func() {
	It("stores a sqlite connection like any other type", func() {
		database := newConnectionTestDB(nil)
		source := filepath.Join(GinkgoT().TempDir(), "catalog.db")

		_, err := newConflictTestService(database).Create(context.Background(), map[string]any{
			"name": "local-catalogue", "type": models.ConnectionTypeSQLite, "url": source,
		})

		Expect(err).NotTo(HaveOccurred())
		stored, err := findConnection(database, "local-catalogue")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{stored.Name, stored.Type, stored.URL}).To(Equal([]string{"local-catalogue", models.ConnectionTypeSQLite, source}))
		Expect(stored.Virtual).To(BeFalse())
	})

	It("keeps a virtual snapshot connection read-only on update and delete", func() {
		snapshot := &models.Connection{
			ID: uuid.New(), Name: "snapshot-orders", Type: models.ConnectionTypeSQLite,
			URL: "file:orders.db?mode=ro", Virtual: true, ReadOnly: true,
		}
		service := newConflictTestService(newConnectionTestDB(nil))
		service.SetVirtual(snapshotProvider{snapshot: snapshot})
		readOnly := `virtual connection "snapshot-orders" is read-only`

		_, err := service.Update(context.Background(), snapshot.Name, map[string]any{
			"name": snapshot.Name, "type": models.ConnectionTypeSQLite, "url": "file:other.db",
		})
		Expect(err).To(MatchError(readOnly))
		Expect(service.Delete(snapshot.Name)).To(MatchError(readOnly))
	})
})
