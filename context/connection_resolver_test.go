package context

import (
	"context"

	"github.com/flanksource/commons-db/models"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("a context's connection resolver", func() {
	known := &models.Connection{Name: "orders", Type: models.ConnectionTypePostgres, URL: "postgres://orders"}
	resolver := func(_ Context, reference string) (*models.Connection, error) {
		if reference == "connection://orders" {
			return known, nil
		}
		return nil, nil
	}

	ginkgo.It("is exposed so another resolver can chain to it", func() {
		Expect(NewContext(context.Background()).ConnectionResolver()).To(BeNil())
		ctx := NewContext(context.Background()).WithConnectionResolver(resolver)
		Expect(ctx.ConnectionResolver()(ctx, "connection://orders")).To(Equal(known))
	})

	ginkgo.It("answers a bare name before it is read as an inline URL", func() {
		ctx := NewContext(context.Background()).WithConnectionResolver(resolver)
		connection, err := HydrateConnectionByURL(ctx, "orders")
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Type).To(Equal(models.ConnectionTypePostgres))
		Expect(connection.URL).To(Equal("postgres://orders"))
	})

	ginkgo.It("leaves a bare name it does not own, and an inline URL, inline", func() {
		ctx := NewContext(context.Background()).WithConnectionResolver(resolver)
		connection, err := HydrateConnectionByURL(ctx, "postgres://inline/app")
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.URL).To(Equal("postgres://inline/app"))
		connection, err = HydrateConnectionByURL(ctx, "unknown")
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Name).To(BeEmpty())
	})
})
