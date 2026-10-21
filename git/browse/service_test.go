package browse_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/git/browse"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBrowse(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git browser") }

func gitAt(dir string, args ...string) string {
	GinkgoHelper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	output, err := command.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(output))
	return strings.TrimSpace(string(output))
}

var _ = Describe("Git browser sources", func() {
	var service *browse.Service
	var sha string
	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		gitAt(dir, "init", "-b", "main")
		Expect(os.Mkdir(filepath.Join(dir, "reports"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "reports", "grants.xlsx"), []byte("workbook bytes"), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "rules.xml"), []byte("<Rules/>"), 0644)).To(Succeed())
		gitAt(dir, "add", "reports/grants.xlsx", "rules.xml")
		gitAt(dir, "commit", "-m", "add files")
		sha = gitAt(dir, "rev-parse", "HEAD")
		connection := models.Connection{Name: "rules", Type: "git", URL: "file://" + dir}
		var err error
		service, err = browse.New(browse.Options{CacheDir: GinkgoT().TempDir(),
			Catalog: func(context.Context) ([]models.Connection, error) { return []models.Connection{connection}, nil },
			Resolve: func(_ context.Context, name string) (models.Connection, error) {
				if name != connection.Name {
					return models.Connection{}, browse.ErrUnknownConnection
				}
				return connection, nil
			}})
		Expect(err).NotTo(HaveOccurred())
	})
	It("lists filtered files and reads exactly the listed commit", func(ctx SpecContext) {
		extensions := []string{".XLSX"}
		_, err := service.Files(ctx, browse.Request{Connection: "rules", Ref: "main", Extensions: extensions})
		Expect(err).NotTo(HaveOccurred())
		Expect(extensions).To(Equal([]string{".XLSX"}))
		page, err := service.Files(ctx, browse.Request{Connection: "rules", Ref: "main", Extensions: []string{".xlsx"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(page).To(Equal(browse.Files{SHA: sha, Paths: []string{"reports/grants.xlsx"}}))
		data, err := service.ReadFile(ctx, browse.Request{Connection: "rules", Ref: sha, Path: "reports/grants.xlsx"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("workbook bytes"))
	})
	It("rejects invalid paths, unknown sources and mismatched pinned repositories", func(ctx SpecContext) {
		for _, path := range []string{"../grants.xlsx", "/grants.xlsx", "reports/../rules.xml", ""} {
			_, err := service.ReadFile(ctx, browse.Request{Connection: "rules", Ref: sha, Path: path})
			Expect(err).To(MatchError(ContainSubstring("path")))
		}
		_, err := service.Refs(ctx, browse.Request{Connection: "missing"})
		Expect(err).To(MatchError(browse.ErrUnknownConnection))
		_, err = service.Refs(ctx, browse.Request{Connection: "rules", Repo: "other"})
		Expect(err).To(MatchError(ContainSubstring("pins")))
	})
	It("returns qualified refs and paginates commits", func(ctx SpecContext) {
		refs, err := service.Refs(ctx, browse.Request{Connection: "rules"})
		Expect(err).NotTo(HaveOccurred())
		Expect(refs.Branches).To(ContainElement(browse.Ref{Name: "main", SHA: sha}))
		page, err := service.Commits(ctx, browse.Request{Connection: "rules", Ref: "main", Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Items).To(HaveLen(1))
		Expect(page.Items[0].SHA).To(Equal(sha))
		Expect(page.NextCursor).To(BeEmpty())
	})
})
