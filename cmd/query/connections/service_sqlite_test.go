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

func (p snapshotProvider) ListConnections() ([]*models.Connection, error) {
	return []*models.Connection{p.snapshot}, nil
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

	It("refuses update and delete of a virtual snapshot connection with 405 read_only", func() {
		snapshot := &models.Connection{
			ID: uuid.New(), Name: "snapshot-orders", Type: models.ConnectionTypeSQLite,
			URL: "file:orders.db?mode=ro", Virtual: true, ReadOnly: true,
		}
		service := newConflictTestService(newConnectionTestDB(nil))
		service.SetVirtual(snapshotProvider{snapshot: snapshot})

		_, err := service.Update(context.Background(), snapshot.Name, map[string]any{
			"name": snapshot.Name, "type": models.ConnectionTypeSQLite, "url": "file:other.db",
		})
		expectReadOnly(err)
		Expect(err).To(MatchError(ContainSubstring(`virtual connection "snapshot-orders" is read-only`)))
		expectReadOnly(service.Delete(snapshot.ID.String()))
	})
})
