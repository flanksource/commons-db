package context

import (
	gocontext "context"
	"time"

	"github.com/flanksource/commons-db/models"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Connection hydration request context", func() {
	ginkgo.It("passes request values and deadlines into secret resolution and rejects canceled warm reads", func() {
		type requestKey struct{}
		request, cancel := gocontext.WithTimeout(gocontext.WithValue(gocontext.Background(), requestKey{}, "active-request"), time.Second)
		defer cancel()
		previous := opReadFunc
		ginkgo.DeferCleanup(func() { opReadFunc = previous })
		var called bool
		opReadFunc = func(ctx Context, _, _ string) (string, error) {
			called = true
			Expect(ctx.Value(requestKey{})).To(Equal("active-request"))
			deadline, _ := ctx.Deadline()
			expected, _ := request.Deadline()
			Expect(deadline).To(Equal(expected))
			Expect(ctx.GetNamespace()).To(Equal("rules"))
			return "example-token", nil
		}
		resolver := New().WithNamespace("rules")
		connection := &models.Connection{Password: "op://request-context/fixture/password"}
		value, err := resolver.HydrateConnection(connection, HydrationOptions{Context: request})
		Expect(err).NotTo(HaveOccurred())
		Expect(called).To(BeTrue())
		Expect(value.Password).To(Equal("example-token"))
		cancel()
		_, err = resolver.HydrateConnection(connection, HydrationOptions{Context: request})
		Expect(err).To(MatchError(gocontext.Canceled))
	})
})
