package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flanksource/commons-db/git/browse"
	"github.com/flanksource/commons-db/git/httpapi"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGitHTTP(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git HTTP handlers") }

var _ = Describe("shared Git HTTP", gitHTTPSpecs)

func gitHTTPSpecs() {
	It("authorizes every operation before parsing inputs or resolving credentials", func() {
		called := false
		api, err := httpapi.New(httpapi.Options{Authorize: func(context.Context) error { return errors.New("forbidden") }, Service: func(context.Context) (*browse.Service, error) { called = true; return nil, nil }})
		Expect(err).NotTo(HaveOccurred())
		for _, operation := range httpapi.Operations {
			response := httptest.NewRecorder()
			api.Handler(operation)(response, httptest.NewRequest(http.MethodGet, "/git/"+operation+"?limit=bad", nil))
			Expect(response.Code).To(Equal(http.StatusForbidden), operation)
		}
		Expect(called).To(BeFalse())
	})
	It("lists redacted connections and rejects invalid requests", func() {
		service, err := browse.New(browse.Options{CacheDir: GinkgoT().TempDir(), Catalog: func(context.Context) ([]models.Connection, error) {
			return []models.Connection{{Name: "rules", Type: "git", URL: "https://user:secret@example.test/rules.git"}, {Name: "db", Type: "postgres"}}, nil
		}, Resolve: func(context.Context, string) (models.Connection, error) {
			return models.Connection{}, browse.ErrUnknownConnection
		}})
		Expect(err).NotTo(HaveOccurred())
		api, err := httpapi.New(httpapi.Options{Authorize: func(context.Context) error { return nil }, Service: func(context.Context) (*browse.Service, error) { return service, nil }})
		Expect(err).NotTo(HaveOccurred())
		response := httptest.NewRecorder()
		api.Handler("connections")(response, httptest.NewRequest(http.MethodGet, "/git/connections", nil))
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(ContainSubstring(`"name":"rules"`))
		Expect(response.Body.String()).NotTo(ContainSubstring("secret"))
		for _, query := range []string{"", "?connection=rules&limit=bad", "?connection=rules&limit=101"} {
			response = httptest.NewRecorder()
			api.Handler("commits")(response, httptest.NewRequest(http.MethodGet, "/git/commits"+query, nil))
			Expect(response.Code).To(Equal(http.StatusBadRequest), query)
		}
		response = httptest.NewRecorder()
		api.Handler("refs")(response, httptest.NewRequest(http.MethodGet, "/git/refs?connection=missing", nil))
		Expect(response.Code).To(Equal(http.StatusNotFound))
	})
}
