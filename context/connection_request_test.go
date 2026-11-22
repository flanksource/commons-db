package context

import (
	gocontext "context"
	"time"

	"github.com/flanksource/commons-db/models"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Connection hydration request context", func() {
	ginkgo.It("passes request values into bounded shared resolution and rejects canceled warm reads", func() {
		type requestKey struct{}
		request, cancel := gocontext.WithTimeout(gocontext.WithValue(gocontext.Background(), requestKey{}, "active-request"), time.Second)
		defer cancel()
		previous := opReadFunc
		ginkgo.DeferCleanup(func() { opReadFunc = previous })
		var requestValue any
		var namespace string
		var resolverDeadline time.Time
		opReadFunc = func(ctx Context, _, _ string) (string, error) {
			requestValue = ctx.Value(requestKey{})
			namespace = ctx.GetNamespace()
			resolverDeadline, _ = ctx.Deadline()
			return "example-token", nil
		}
		resolver := New().WithNamespace("rules")
		connection := &models.Connection{Password: "op://request-context/fixture/password"}
		value, err := resolver.HydrateConnection(connection, HydrationOptions{Context: request})
		Expect(err).NotTo(HaveOccurred())
		Expect(requestValue).To(Equal("active-request"))
		Expect(namespace).To(Equal("rules"))
		requestDeadline, _ := request.Deadline()
		Expect(resolverDeadline).To(BeTemporally(">", requestDeadline))
		Expect(value.Password).To(Equal("example-token"))
		cancel()
		_, err = resolver.HydrateConnection(connection, HydrationOptions{Context: request})
		Expect(err).To(MatchError(gocontext.Canceled))
	})
})
