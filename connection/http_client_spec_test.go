package connection_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Creating an HTTP client", func() {
	It("sends basic authentication with the configured credentials", func(ctx SpecContext) {
		const username, password = "api-user", "api-password"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || user != username || pass != password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		DeferCleanup(server.Close)
		client, err := connection.CreateHTTPClient(nil, connection.HTTPConnection{HTTPBasicAuth: types.HTTPBasicAuth{
			Authentication: types.Authentication{
				Username: types.EnvVar{ValueStatic: username},
				Password: types.EnvVar{ValueStatic: password},
			},
		}})
		Expect(err).NotTo(HaveOccurred())
		response, err := client.R(ctx).Get(server.URL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)
		Expect(response.StatusCode).To(Equal(http.StatusNoContent))
	})
})
