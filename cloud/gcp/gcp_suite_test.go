package gcp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2"
)

func TestGCP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cloud/gcp")
}

const testToken = "test-access-token"

type staticToken string

func (s staticToken) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: string(s), Expiry: time.Now().Add(time.Hour)}, nil
}

type recordedRequest struct {
	Method string
	Path   string
	Query  map[string][]string
	Header http.Header
	Body   []byte
}

// fakeAPI is an httptest server that records every request before handing it to handler.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func newFakeAPI(handler func(w http.ResponseWriter, r *http.Request, body []byte)) *fakeAPI {
	api := &fakeAPI{}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		Expect(err).NotTo(HaveOccurred())
		api.mu.Lock()
		api.requests = append(api.requests, recordedRequest{
			Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone(), Body: body,
		})
		api.mu.Unlock()
		handler(w, r, body)
	}))
	return api
}

func (f *fakeAPI) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
