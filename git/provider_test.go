package git_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// apiCall is one request a fake provider API received.
type apiCall struct {
	Method, Path, Query, Authorization string
	Body                               map[string]any
}

// fakeAPI is an HTTPS provider API answering from a route table keyed by
// "METHOD path" and recording every call.
type fakeAPI struct {
	Server *httptest.Server
	mu     sync.Mutex
	calls  []apiCall
}

type apiReply struct {
	Status  int
	Headers map[string]string
	Body    string
}

func newFakeAPI(routes map[string]func(r *http.Request) apiReply) *fakeAPI {
	GinkgoHelper()
	api := &fakeAPI{}
	api.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.record(r)
		route, ok := routes[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			http.Error(w, "no route "+r.Method+" "+r.URL.EscapedPath(), http.StatusNotFound)
			return
		}
		reply := route(r)
		for key, value := range reply.Headers {
			w.Header().Set(key, strings.ReplaceAll(value, "{base}", api.Server.URL))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(max(reply.Status, http.StatusOK))
		_, _ = io.WriteString(w, strings.ReplaceAll(reply.Body, "{base}", api.Server.URL))
	}))
	DeferCleanup(api.Server.Close)
	return api
}

func (a *fakeAPI) record(r *http.Request) {
	call := apiCall{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Authorization: r.Header.Get("Authorization")}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		Expect(json.Unmarshal(raw, &call.Body)).To(Succeed())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, call)
}

func (a *fakeAPI) Calls() []apiCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]apiCall(nil), a.calls...)
}

func reply(body string) func(*http.Request) apiReply {
	return func(*http.Request) apiReply { return apiReply{Body: body} }
}

func providerClient(connectionType, url string, properties map[string]string) *git.Client {
	GinkgoHelper()
	client, err := git.FromConnection(dbcontext.New(), models.Connection{Type: connectionType, URL: url,
		Password: "test-token", InsecureTLS: true, Properties: types.JSONStringMap(properties)})
	Expect(err).ToNot(HaveOccurred())
	return client
}

func repoURL(raw, hint string) git.RepoURL {
	GinkgoHelper()
	parsed, err := git.ParseRepoURL(raw, hint)
	Expect(err).ToNot(HaveOccurred())
	return parsed
}

var _ = Describe("REST client rules", func() {
	It("refuses an HTTP API URL", func() {
		_, err := git.FromConnection(dbcontext.New(), models.Connection{Type: models.ConnectionTypeGithub,
			URL: "http://example.invalid", Password: "test-token"})
		Expect(err).To(MatchError(ContainSubstring("HTTPS URL")))
	})

	It("refuses a redirect to a non-HTTPS URL", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET /user": func(*http.Request) apiReply {
			return apiReply{Status: http.StatusFound, Headers: map[string]string{"Location": "http://example.invalid/user"}}
		}})
		err := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider.Verify(ctx)
		Expect(err).To(MatchError(ContainSubstring("non-HTTPS URL")))
	})

	It("truncates an error body to 512 bytes", func(ctx SpecContext) {
		api := newFakeAPI(map[string]func(*http.Request) apiReply{"GET /user": func(*http.Request) apiReply {
			return apiReply{Status: http.StatusInternalServerError, Body: strings.Repeat("x", 600) + "TAIL"}
		}})
		err := providerClient(models.ConnectionTypeGithub, api.Server.URL, nil).Provider.Verify(ctx)
		var apiErr *git.APIError
		Expect(err).To(BeAssignableToTypeOf(apiErr))
		Expect(err.(*git.APIError).StatusCode).To(Equal(http.StatusInternalServerError))
		Expect(err.Error()).To(ContainSubstring(strings.Repeat("x", 512)))
		Expect(err.Error()).ToNot(ContainSubstring(strings.Repeat("x", 513)))
		Expect(err.Error()).ToNot(ContainSubstring("TAIL"))
	})
})
