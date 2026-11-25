package azure_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons/http/middlewares/azuread"
)

func TestAzure(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cloud/azure")
}

const fakeAccessToken = "fake-aad-token"

// fakeAAD is an Azure AD token endpoint that records the scopes requested.
type fakeAAD struct {
	*httptest.Server
	mu     sync.Mutex
	scopes []string
}

func newFakeAAD() *fakeAAD {
	f := &fakeAAD{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Expect(r.ParseForm()).To(Succeed())
		f.mu.Lock()
		f.scopes = append(f.scopes, r.PostForm.Get("scope"))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fakeAccessToken, "token_type": "Bearer", "expires_in": 3600})
	}))
	return f
}

func (f *fakeAAD) Scopes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scopes...)
}

func (f *fakeAAD) Credential() azuread.Credential {
	return azuread.ClientSecret{TenantID: "tenant", ClientID: "client", ClientSecret: "secret", Authority: f.URL, HTTPClient: f.Client()}
}

// recorded is the request seen by a fake service.
type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Auth   string
	Body   map[string]any
}

func record(r *http.Request) recorded {
	rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Auth: r.Header.Get("Authorization")}
	Expect(json.NewDecoder(r.Body).Decode(&rec.Body)).To(Succeed())
	return rec
}
