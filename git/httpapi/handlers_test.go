package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/browse"
	"github.com/flanksource/commons-db/git/httpapi"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGitHTTP(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git HTTP handlers") }

var _ = Describe("shared Git HTTP", gitHTTPSpecs)

func allow(context.Context) error { return nil }

func forbid(context.Context) error { return errors.New("forbidden") }

func gitHTTPSpecs() {
	It("authorizes every operation and action before parsing inputs or resolving credentials", func() {
		called := false
		service := func(context.Context) (*browse.Service, error) { called = true; return nil, nil }
		readers, err := httpapi.New(httpapi.Options{Authorize: forbid, AuthorizeActions: allow, Service: service})
		Expect(err).NotTo(HaveOccurred())
		for _, operation := range httpapi.Operations {
			response := httptest.NewRecorder()
			readers.Handler(operation)(response, httptest.NewRequest(http.MethodGet, "/git/"+operation+"?limit=bad", nil))
			Expect(response.Code).To(Equal(http.StatusForbidden), operation)
		}
		writers, err := httpapi.New(httpapi.Options{Authorize: allow, AuthorizeActions: forbid, Service: service})
		Expect(err).NotTo(HaveOccurred())
		for _, action := range httpapi.Actions {
			response := httptest.NewRecorder()
			writers.Handler(action)(response, httptest.NewRequest(http.MethodPost, "/git/"+action, strings.NewReader("not json")))
			Expect(response.Code).To(Equal(http.StatusForbidden), action)
		}
		Expect(called).To(BeFalse())
		_, err = httpapi.New(httpapi.Options{Authorize: allow, Service: service})
		Expect(err).To(MatchError(ContainSubstring("action authorization")))
	})
	It("lists redacted connections and rejects invalid requests", func() {
		service, err := browse.New(browse.Options{CacheDir: GinkgoT().TempDir(), Catalog: func(context.Context) ([]models.Connection, error) {
			return []models.Connection{{Name: "rules", Type: "git", URL: "https://user:secret@example.test/rules.git"}, {Name: "db", Type: "postgres"}}, nil
		}, Resolve: func(context.Context, string) (models.Connection, error) {
			return models.Connection{}, browse.ErrUnknownConnection
		}})
		Expect(err).NotTo(HaveOccurred())
		api, err := httpapi.New(httpapi.Options{Authorize: allow, AuthorizeActions: allow, Service: func(context.Context) (*browse.Service, error) { return service, nil }})
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
	Describe("the git cache", gitCacheHTTPSpecs)
}

func gitCacheHTTPSpecs() {
	const rulesURL = "https://example.test/acme/rules.git"
	var (
		cacheDir string
		key      string
		api      *httpapi.API
	)
	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		parsed, err := git.ParseRepoURL(rulesURL, "")
		Expect(err).NotTo(HaveOccurred())
		key = parsed.CacheKey()
		mirror := filepath.Join(cacheDir, key, "mirror.git")
		Expect(os.MkdirAll(mirror, 0o755)).To(Succeed())
		Expect(exec.Command("git", "init", "--quiet", "--bare", mirror).Run()).To(Succeed())
		rules := models.Connection{Name: "rules", Type: models.ConnectionTypeGit, URL: rulesURL}
		service, err := browse.New(browse.Options{CacheDir: cacheDir, PinTTL: time.Hour,
			Catalog: func(context.Context) ([]models.Connection, error) { return []models.Connection{rules}, nil },
			Resolve: func(_ context.Context, name string) (models.Connection, error) {
				if name != rules.Name {
					return models.Connection{}, browse.ErrUnknownConnection
				}
				return rules, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		api, err = httpapi.New(httpapi.Options{Authorize: allow, AuthorizeActions: allow, Service: func(context.Context) (*browse.Service, error) { return service, nil }})
		Expect(err).NotTo(HaveOccurred())
	})
	call := func(method, operation, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		api.Handler(operation)(response, httptest.NewRequest(method, "/git/"+operation, strings.NewReader(body)))
		return response
	}

	It("lists each entry with the catalog connection caching it", func() {
		response := call(http.MethodGet, "cache", "")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var inventory browse.CacheInventory
		Expect(json.Unmarshal(response.Body.Bytes(), &inventory)).To(Succeed())
		Expect(inventory.ConnectionErrors).To(BeEmpty())
		Expect(inventory.Entries).To(HaveLen(1))
		Expect(inventory.Entries[0].Key).To(Equal(key))
		Expect(inventory.Entries[0].Owner).To(Equal(key))
		Expect(inventory.Entries[0].Connection).To(Equal("rules"))
		Expect(inventory.Entries[0].Mirror).NotTo(BeNil())
	})

	It("prunes and evicts a mirror by its cache key", func() {
		response := call(http.MethodPost, "cache-prune", `{"owner":"`+key+`"}`)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`"key":"` + key + `"`))

		response = call(http.MethodPost, "cache-evict", `{"owner":"`+key+`"}`)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(Equal(`{"owner":"` + key + `"}`))
		Expect(filepath.Join(cacheDir, key, "mirror.git")).NotTo(BeAnExistingFile())
	})

	DescribeTable("rejects an invalid action",
		func(method, action, body string, status int) {
			Expect(call(method, action, body).Code).To(Equal(status))
		},
		Entry("action as GET", http.MethodGet, "cache-evict", "", http.StatusBadRequest),
		Entry("unknown field", http.MethodPost, "cache-evict", `{"owner":"x","force":true}`, http.StatusBadRequest),
		Entry("missing owner", http.MethodPost, "cache-prune", `{}`, http.StatusBadRequest),
		Entry("owner without a mirror", http.MethodPost, "cache-evict", `{"owner":"absent-0123456789ab"}`, http.StatusBadRequest),
		Entry("escaping owner", http.MethodPost, "cache-evict", `{"owner":"../escape"}`, http.StatusBadRequest),
		Entry("rebind without a connection", http.MethodPost, "cache-rebind", `{"owner":"x"}`, http.StatusBadRequest),
		Entry("rebind of a repository that never shares a mirror", http.MethodPost, "cache-rebind", `{"connection":"rules"}`, http.StatusBadRequest),
	)
}
