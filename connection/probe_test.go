package connection_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("model connection probes", func() {
	DescribeTable("tests an account API without a repository",
		func(connectionType, basePath, expectedPath string) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer GinkgoRecover()
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
			result, err := connection.ProbeModel(context.Background(), models.Connection{Type: connectionType, URL: server.URL + basePath, Password: "test-token", InsecureTLS: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.OK).To(BeTrue())
		},
		Entry("GitHub", models.ConnectionTypeGithub, "", "/user"),
		Entry("GitLab", models.ConnectionTypeGitlab, "/gitlab", "/gitlab/api/v4/user"),
		Entry("Azure DevOps", models.ConnectionTypeAzureDevops, "/org", "/org/_apis/projects"),
	)

	It("rejects an account token sent over HTTP", func(ctx SpecContext) {
		_, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeGithub, URL: "http://example.invalid", Password: "test-token"})
		Expect(err).To(MatchError(ContainSubstring("HTTPS URL")))
	})

	It("rejects a redirect from HTTPS to HTTP", func(ctx SpecContext) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			http.Redirect(w, r, "http://example.invalid/unsafe", http.StatusFound)
		}))
		defer server.Close()
		_, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeGithub, URL: server.URL, Password: "test-token", InsecureTLS: true})
		Expect(err).To(MatchError(ContainSubstring("non-HTTPS URL")))
	})

	It("pings SQLite", func(ctx SpecContext) {
		path := filepath.Join(GinkgoT().TempDir(), "catalog.db")
		result, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeSQLite, URL: path})
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(connection.ProbeResult{OK: true}))
	})

	It("lists references from a saved Git repository", func(ctx SpecContext) {
		repositoryPath := filepath.Join(GinkgoT().TempDir(), "source")
		repository, err := git.PlainInit(repositoryPath, false)
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("example\n"), 0600)).To(Succeed())
		worktree, err := repository.Worktree()
		Expect(err).ToNot(HaveOccurred())
		_, err = worktree.Add("README.md")
		Expect(err).ToNot(HaveOccurred())
		_, err = worktree.Commit("Initial commit", &git.CommitOptions{Author: &object.Signature{Name: "Example", Email: "example@example.org", When: time.Now()}})
		Expect(err).ToNot(HaveOccurred())
		result, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeGit, URL: repositoryPath})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.OK).To(BeTrue())
	})

	It("lists S3 buckets using a saved endpoint and credentials", func(ctx SpecContext) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.Method).To(Equal(http.MethodGet))
			Expect(r.URL.Path).To(Equal("/"))
			Expect(r.Header.Get("Authorization")).To(ContainSubstring("test-access"))
			w.Header().Set("Content-Type", "application/xml")
			_, err := w.Write([]byte(`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Buckets></Buckets></ListAllMyBucketsResult>`))
			Expect(err).ToNot(HaveOccurred())
		}))
		defer server.Close()
		result, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeS3, URL: server.URL,
			Username: "test-access", Password: "test-secret", Properties: types.JSONStringMap{"region": "us-east-1"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.OK).To(BeTrue())
	})

	It("tests a scoped S3 bucket without account-wide list permission", func(ctx SpecContext) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.Method).To(Equal(http.MethodHead))
			Expect(r.URL.Path).To(Equal("/source-bucket"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		result, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeS3, URL: server.URL,
			Username: "test-access", Password: "test-secret", Properties: types.JSONStringMap{"region": "us-east-1", "bucket": "source-bucket", "usePathStyle": "true"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.OK).To(BeTrue())
	})

	It("rejects an invalid S3 path-style setting", func(ctx SpecContext) {
		_, err := connection.ProbeModel(ctx, models.Connection{Type: models.ConnectionTypeS3,
			Properties: types.JSONStringMap{"usePathStyle": "sometimes"}})
		Expect(err).To(MatchError(ContainSubstring("invalid S3 usePathStyle")))
	})
})
