package report_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/cmd/query/schedules/report"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// renderRequest is what the facet server received, unpacked.
type renderRequest struct {
	format    string
	entryFile string
	timeout   float64
	data      map[string]any
	archive   map[string]string
	apiKey    string
}

// facetServer stands in for `facet serve`, recording the request and answering
// the way the real one does: HTML in the body, everything else as a pointer to
// a stored result that has to be fetched separately.
func facetServer(format string, rendered []byte) (*httptest.Server, *renderRequest) {
	recorded := &renderRequest{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /render", func(w http.ResponseWriter, r *http.Request) {
		recorded.apiKey = r.Header.Get("X-API-Key")
		Expect(r.ParseMultipartForm(1 << 20)).To(Succeed())

		var options map[string]any
		Expect(json.Unmarshal([]byte(r.FormValue("options")), &options)).To(Succeed())
		recorded.format, _ = options["format"].(string)
		recorded.entryFile, _ = options["entryFile"].(string)
		recorded.timeout, _ = options["timeout"].(float64)

		Expect(json.Unmarshal([]byte(r.FormValue("data")), &recorded.data)).To(Succeed())

		file, _, err := r.FormFile("archive")
		Expect(err).ToNot(HaveOccurred())
		defer file.Close()
		recorded.archive = unpack(file)

		if format == "html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(rendered)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "/results/abc123"})
	})

	mux.HandleFunc("GET /results/abc123", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(rendered)
	})

	return httptest.NewServer(mux), recorded
}

func unpack(reader io.Reader) map[string]string {
	gzipReader, err := gzip.NewReader(reader)
	Expect(err).ToNot(HaveOccurred())
	defer gzipReader.Close()

	files := map[string]string{}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		Expect(err).ToNot(HaveOccurred())
		content, err := io.ReadAll(tarReader)
		Expect(err).ToNot(HaveOccurred())
		files[header.Name] = string(content)
	}
	return files
}

// templateDir writes a minimal template so the archive has something to carry.
func templateDir() string {
	dir := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"t"}`), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "Report.tsx"), []byte("export default () => null"), 0o600)).To(Succeed())

	// Build output must not be shipped: it is large and the server rebuilds it.
	Expect(os.MkdirAll(filepath.Join(dir, "node_modules", "react"), 0o700)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "node_modules", "react", "index.js"), []byte("x"), 0o600)).To(Succeed())
	return dir
}

var _ = Describe("Rendering through the facet API", func() {
	var srcDir string

	BeforeEach(func() { srcDir = templateDir() })

	It("sends the archive, data and options, and reads HTML straight from the body", func() {
		server, recorded := facetServer("html", []byte("<html>report</html>"))
		defer server.Close()

		rendered, err := report.RenderHTTP(context.Background(),
			report.Server{BaseURL: server.URL, Token: "secret"},
			map[string]any{"title": "Nightly"},
			"html", srcDir, "Report.tsx", report.RenderOptions{Timeout: 90 * time.Second})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(rendered)).To(Equal("<html>report</html>"))

		Expect(recorded.apiKey).To(Equal("secret"))
		Expect(recorded.format).To(Equal("html"))
		Expect(recorded.entryFile).To(Equal("Report.tsx"))
		Expect(recorded.timeout).To(BeEquivalentTo(90_000), "timeout is sent in milliseconds")
		Expect(recorded.data).To(HaveKeyWithValue("title", "Nightly"))

		Expect(recorded.archive).To(HaveKey("package.json"))
		Expect(recorded.archive).To(HaveKey("Report.tsx"))
		Expect(recorded.archive).ToNot(HaveKey("node_modules/react/index.js"),
			"node_modules must not be shipped to the render server")
	})

	// A PDF comes back as {"url": ...}; reading the first response as bytes
	// would hand the caller a JSON pointer and call it a PDF.
	It("follows the result url for a PDF", func() {
		server, recorded := facetServer("pdf", []byte("%PDF-1.7 body"))
		defer server.Close()

		rendered, err := report.RenderHTTP(context.Background(),
			report.Server{BaseURL: server.URL},
			map[string]any{}, "pdf", srcDir, "Report.tsx", report.RenderOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(rendered)).To(HavePrefix("%PDF-"))
		Expect(recorded.format).To(Equal("pdf"))
	})

	It("reports the server's own error rather than a bare status", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error":{"code":"RENDER_FAILED","message":"missing export"}}`)
		}))
		defer server.Close()

		_, err := report.RenderHTTP(context.Background(), report.Server{BaseURL: server.URL},
			map[string]any{}, "pdf", srcDir, "Report.tsx", report.RenderOptions{})
		Expect(err).To(MatchError(ContainSubstring("422")))
		Expect(err).To(MatchError(ContainSubstring("missing export")))
	})

	It("fails when the response carries no result url", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{}`)
		}))
		defer server.Close()

		_, err := report.RenderHTTP(context.Background(), report.Server{BaseURL: server.URL},
			map[string]any{}, "pdf", srcDir, "Report.tsx", report.RenderOptions{})
		Expect(err).To(MatchError(ContainSubstring("no result url")))
	})
})

var _ = Describe("BuildArchive", func() {
	It("refuses a directory that is not there", func() {
		_, err := report.BuildArchive(filepath.Join(GinkgoT().TempDir(), "absent"))
		Expect(err).To(HaveOccurred())
	})

	It("produces a readable gzip tar", func() {
		archive, err := report.BuildArchive(templateDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(unpack(bytes.NewReader(archive))).To(HaveKey("package.json"))
	})
})
