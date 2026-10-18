package git_test

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git Suite")
}

// fixtureAuthor is the identity every fixture commit is made with.
var fixtureAuthor = []string{
	"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.org",
	"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.org",
}

// gitFixture runs the git binary for fixture setup, failing the spec on error.
func gitFixture(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fixtureAuthor...)
	out, err := cmd.CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// bareRepo is a fixture remote: main (2 commits), feature (1 more commit),
// annotated tag v1 and lightweight tag v1-lite on main's first commit.
type bareRepo struct {
	Root    string // directory holding the bare repository
	Path    string // bare repository path
	Main    string // main tip SHA
	First   string // first main commit SHA (tagged)
	Feature string // feature tip SHA
	TagObj  string // annotated tag object SHA
}

func (b bareRepo) FileURL() string { return "file://" + b.Path }

func newBareRepo(name string) bareRepo {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	work := filepath.Join(root, "work")
	Expect(os.MkdirAll(work, 0o755)).To(Succeed())
	gitFixture(work, "init", "-q", "-b", "main")
	writeFixtureFile(work, "README.md", "first\n")
	gitFixture(work, "add", "-A")
	gitFixture(work, "commit", "-q", "-m", "first")
	first := gitFixture(work, "rev-parse", "HEAD")
	gitFixture(work, "tag", "-a", "v1", "-m", "release v1")
	gitFixture(work, "tag", "v1-lite")
	writeFixtureFile(work, "README.md", "second\n")
	gitFixture(work, "commit", "-q", "-am", "second")
	main := gitFixture(work, "rev-parse", "HEAD")
	gitFixture(work, "checkout", "-q", "-b", "feature")
	writeFixtureFile(work, "feature.txt", "feature\n")
	gitFixture(work, "add", "-A")
	gitFixture(work, "commit", "-q", "-m", "feature")
	feature := gitFixture(work, "rev-parse", "HEAD")
	bare := filepath.Join(root, name+".git")
	gitFixture(root, "clone", "-q", "--bare", work, bare)
	gitFixture(bare, "config", "http.receivepack", "true")
	gitFixture(bare, "config", "receive.advertisePushOptions", "true")
	gitFixture(bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bareRepo{Root: root, Path: bare, Main: main, First: first, Feature: feature,
		TagObj: gitFixture(bare, "rev-parse", "refs/tags/v1")}
}

func writeFixtureFile(dir, name, content string) {
	GinkgoHelper()
	Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)).To(Succeed())
}

// installPushOptionHook records every push option the remote receives.
func (b bareRepo) installPushOptionHook() string {
	GinkgoHelper()
	record := filepath.Join(b.Root, "push-options.txt")
	hook := "#!/bin/sh\ni=0\nwhile [ $i -lt \"${GIT_PUSH_OPTION_COUNT:-0}\" ]; do\n" +
		"  eval \"echo \\$GIT_PUSH_OPTION_$i\" >> '" + record + "'\n  i=$((i+1))\ndone\n"
	Expect(os.WriteFile(filepath.Join(b.Path, "hooks", "pre-receive"), []byte(hook), 0o755)).To(Succeed())
	return record
}

// httpRemote serves bare repositories over smart HTTP through
// git-http-backend behind Basic auth, recording each request.
type httpRemote struct {
	Server   *httptest.Server
	User     string
	Password string
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Method, Path, Query, Authorization string
}

func newHTTPRemote(root string, useTLS bool) *httpRemote {
	GinkgoHelper()
	remote := &httpRemote{User: "fixture-user", Password: "s3cret-fixture-pass"}
	backend := &cgi.Handler{
		Path: filepath.Join(gitFixture(root, "--exec-path"), "git-http-backend"),
		Root: "/",
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote.record(r)
		if user, pass, ok := r.BasicAuth(); !ok || user != remote.User || pass != remote.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	})
	if useTLS {
		remote.Server = httptest.NewTLSServer(handler)
	} else {
		remote.Server = httptest.NewServer(handler)
	}
	DeferCleanup(remote.Server.Close)
	return remote
}

func (h *httpRemote) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, recordedRequest{Method: r.Method, Path: r.URL.Path,
		Query: r.URL.RawQuery, Authorization: r.Header.Get("Authorization")})
}

func (h *httpRemote) Requests() []recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedRequest(nil), h.requests...)
}

// uploadPacks counts pack negotiations (fetches); ls-remote only GETs info/refs.
func (h *httpRemote) uploadPacks() int {
	count := 0
	for _, request := range h.Requests() {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/git-upload-pack") {
			count++
		}
	}
	return count
}

func (h *httpRemote) URL(repo bareRepo) string {
	return h.Server.URL + "/" + filepath.Base(repo.Path)
}

// CAPEM returns the TLS server's certificate as a PEM CA bundle.
func (h *httpRemote) CAPEM() string {
	cert := h.Server.Certificate()
	Expect(cert).ToNot(BeNil())
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

// argvRecorder puts a git wrapper first on PATH that logs every argv before
// running the real git, so specs can assert no secret reached a command line.
func argvRecorder() string {
	GinkgoHelper()
	realGit, err := exec.LookPath("git")
	Expect(err).ToNot(HaveOccurred())
	dir := GinkgoT().TempDir()
	log := filepath.Join(dir, "argv.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> '%s'\nexec '%s' \"$@\"\n", log, realGit)
	Expect(os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755)).To(Succeed())
	GinkgoT().Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}
