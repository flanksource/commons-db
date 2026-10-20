package git_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	commonshttp "github.com/flanksource/commons/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider transport ownership", func() {
	It("drains JSON responses and reuses transport without retaining authentication", func() {
		var connections atomic.Int64
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer example-first" && r.Header.Get("Authorization") != "Bearer example-rotated" {
				http.Error(w, "unexpected credentials", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "[]"+strings.Repeat(" ", 2048))
		}))
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		}
		server.StartTLS()
		DeferCleanup(server.Close)
		pool, err := commonshttp.NewTransportPool(commonshttp.TransportPoolOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(pool.Close)
		options := []git.ClientOptions{{TransportPool: pool, SecurityNamespace: "example-scope"}}
		for _, password := range []string{"example-first", "example-first", "example-rotated", "example-first"} {
			client, err := git.FromConnection(dbcontext.NewContext(context.Background()), models.Connection{Name: "example", Type: models.ConnectionTypeGithub, URL: server.URL, Password: password, InsecureTLS: true}, options...)
			Expect(err).NotTo(HaveOccurred())
			_, err = client.Provider.ListRepositories(context.Background(), git.ListRepositoriesOptions{})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(connections.Load()).To(Equal(int64(2)))
		Expect(options[0].SecurityNamespace).To(Equal("example-scope"))
	})
})
