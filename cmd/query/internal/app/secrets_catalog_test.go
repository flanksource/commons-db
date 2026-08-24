package app

import (
	"net/http"
	"net/http/httptest"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
)

type fakeOnePasswordCatalog struct{}

func (fakeOnePasswordCatalog) Vaults() ([]dbcontext.OnePasswordVault, error) {
	return []dbcontext.OnePasswordVault{{ID: "vault-id", Name: "Production"}}, nil
}

func (fakeOnePasswordCatalog) Items(vaultID string) ([]dbcontext.OnePasswordItem, error) {
	Expect(vaultID).To(Equal("vault-id"))
	return []dbcontext.OnePasswordItem{{ID: "item-id", Name: "Database"}}, nil
}

func (fakeOnePasswordCatalog) Fields(vaultID, itemID string) ([]dbcontext.OnePasswordField, error) {
	Expect(vaultID).To(Equal("vault-id"))
	Expect(itemID).To(Equal("item-id"))
	return []dbcontext.OnePasswordField{{
		ID: "password", Label: "Password", Reference: "op://Production/Database/password",
	}}, nil
}

var _ = Describe("server-backed secret catalogs", func() {
	var handler http.Handler

	BeforeEach(func() {
		handler = newSecretsHandler(secretsHandlerOptions{
			Prefix:      "/api/v1",
			OnePassword: fakeOnePasswordCatalog{},
			Kube: func() (kubernetes.Interface, error) {
				Fail("1Password catalog routes must not require Kubernetes")
				return nil, nil
			},
			Next: http.NotFoundHandler(),
		})
	})

	It("serves vault, item, and field metadata without Kubernetes", func() {
		for _, path := range []string{
			"/api/v1/secrets/onepassword/vaults",
			"/api/v1/secrets/onepassword/items?vault=vault-id",
			"/api/v1/secrets/onepassword/fields?vault=vault-id&item=item-id",
		} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			Expect(response.Code).To(Equal(http.StatusOK), path)
			Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))
		}
	})

	It("rejects missing item catalog parameters", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/secrets/onepassword/items", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("vault is required"))
	})
})
