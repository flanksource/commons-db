package profiles

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type requestTenantKey struct{}

var _ = Describe("BeforeExecute over HTTP", func() {
	// A host prepares data for the request in front of it: which tenant's store
	// holds the stream is something a middleware bound to that request, and the
	// server's own context cannot know it.
	It("runs under the request's context, so what a middleware bound to it reaches the hook", func() {
		var events []string
		query.RegisterProvider(hookedProvider{events: &events})
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), hookedProfile())).To(Succeed())
		var tenants []any
		service, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			BeforeExecute: func(ctx context.Context, _ []ReadRequest) (func(), error) {
				tenants = append(tenants, ctx.Value(requestTenantKey{}))
				return func() {}, nil
			},
		})
		Expect(err).ToNot(HaveOccurred())
		handler, err := service.Handler("/api/v1", &nextMarker{})
		Expect(err).ToNot(HaveOccurred())

		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/stream-results?stream=run-1", nil)
		request = request.WithContext(context.WithValue(request.Context(), requestTenantKey{}, "lab"))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(tenants).To(Equal([]any{"lab"}))
	})
})
