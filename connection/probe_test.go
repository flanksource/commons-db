package connection_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("model connection probes", func() {
	DescribeTable("tests an account API without a repository",
		func(connectionType, basePath, expectedPath string) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal(expectedPath))
				if connectionType == models.ConnectionTypeAzureDevops {
					_, token, ok := r.BasicAuth()
					Expect(ok).To(BeTrue())
					Expect(token).To(Equal("test-token"))
				} else {
					Expect(r.Header.Get("Authorization")).To(ContainSubstring("test-token"))
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			result, err := connection.ProbeModel(context.Background(), models.Connection{Type: connectionType, URL: server.URL + basePath, Password: "test-token"})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.OK).To(BeTrue())
		},
		Entry("GitHub", models.ConnectionTypeGithub, "", "/user"),
		Entry("GitLab", models.ConnectionTypeGitlab, "/gitlab", "/gitlab/api/v4/user"),
		Entry("Azure DevOps", models.ConnectionTypeAzureDevops, "/org", "/org/_apis/projects"),
	)

	It("pings SQLite", func(ctx SpecContext) {
		path := filepath.Join(GinkgoT().TempDir(), "catalog.db")
		result, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeSQLite, URL: path})
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(connection.ProbeResult{OK: true}))
	})
})
