// Specs for the query app mounted under a host's prefix, as a host that
// composes it over a trace store's API (traceserve.Handler) serves it.
package www_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/www"
)

const mount = "/query/run-1"

func get(server *httptest.Server, path string) *http.Response {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(server.URL + mount + path)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(response.Body.Close)
	return response
}

var _ = Describe("the query app under a mount", func() {
	var server *httptest.Server

	BeforeEach(func() {
		app, err := www.Handler()
		Expect(err).ToNot(HaveOccurred())
		server = httptest.NewServer(http.StripPrefix(mount, app))
		DeferCleanup(server.Close)
	})

	It("links a kind's window to the app's page for its result profile, under the mount", func() {
		window := "stream=s1&afterSeq=1&toSeq=3"
		response := get(server, "/traces/http_proxy?"+window)
		Expect(response.StatusCode).To(Equal(http.StatusFound))
		location := response.Header.Get("Location")
		base, err := url.Parse(server.URL + mount + "/traces/http_proxy")
		Expect(err).ToNot(HaveOccurred())
		Expect(base.ResolveReference(&url.URL{Path: strings.SplitN(location, "?", 2)[0]}).Path).To(Equal(mount + "/profile-traces-http-proxy"))
		Expect(location).To(HaveSuffix("?" + window))
	})
})
