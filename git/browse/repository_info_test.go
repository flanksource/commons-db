package browse_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/flanksource/commons-db/git/browse"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Opened repository metadata", func() {
	It("preserves provider web metadata without a second catalog lookup", func(ctx SpecContext) {
		var calls atomic.Int32
		var server *httptest.Server
		server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/api/v4/groups/acme/projects" {
				http.NotFound(w, request)
				return
			}
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"path":"rules","path_with_namespace":"acme/rules","http_url_to_repo":%q,"web_url":%q,"namespace":{"full_path":"acme"}}]`, server.URL+"/acme/rules.git", server.URL+"/acme/rules")
		}))
		DeferCleanup(server.Close)
		service, err := browse.New(browse.Options{CacheDir: GinkgoT().TempDir(), Resolve: func(context.Context, string) (models.Connection, error) {
			return models.Connection{Type: "gitlab", URL: server.URL, Password: "fixture-token", InsecureTLS: true, Properties: map[string]string{"group": "acme"}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		for range 2 {
			opened, err := service.Open(ctx, browse.Request{Connection: "source", Repo: "rules"})
			Expect(err).NotTo(HaveOccurred())
			Expect(opened.Info.WebURL).To(Equal(server.URL + "/acme/rules"))
		}
		Expect(calls.Load()).To(Equal(int32(1)))
	})
})
