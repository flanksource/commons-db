package connections

import (
	"context"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type writableCatalog struct {
	configCatalog
	saves, deletes int
}

func (*writableCatalog) Writable() bool { return true }
func (c *writableCatalog) SaveConnection(_ context.Context, connection *models.Connection) (*models.Connection, error) {
	c.saves++
	return connection, nil
}
func (c *writableCatalog) DeleteConnection(context.Context, string) error {
	c.deletes++
	return nil
}

var _ = Describe("writable provider row permissions", func() {
	DescribeTable("rejects update and delete before invoking a writable provider",
		func(virtual, readOnly bool) {
			catalog := &writableCatalog{configCatalog: configCatalog{connections: []*models.Connection{{
				ID: uuid.New(), Name: "protected", Type: models.ConnectionTypeHTTP,
				Virtual: virtual, ReadOnly: readOnly,
			}}}}
			service, err := New(Options{
				Context:    func() dbcontext.Context { return dbcontext.New() },
				DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
				Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
				Virtual:    catalog,
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = service.Update(context.Background(), "protected", map[string]any{"name": "edited", "type": models.ConnectionTypeHTTP})
			expectReadOnly(err)
			expectReadOnly(service.Delete("protected"))
			Expect([]int{catalog.saves, catalog.deletes}).To(Equal([]int{0, 0}))
			Expect(catalog.connections[0].Name).To(Equal("protected"))
		},
		Entry("read-only row", false, true),
		Entry("virtual row", true, false),
		Entry("virtual and read-only row", true, true),
	)
})
