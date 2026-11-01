package localfiles

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLocalFiles(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Local files") }

var _ = Describe("Filesystem browsing", func() {
	It("opens a file's parent and follows directory symlinks", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "example ?#.sqlite")
		Expect(os.WriteFile(path, []byte("private"), 0600)).To(Succeed())
		listing, err := list(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(listing.Path).To(Equal(dir))
		Expect(listing.Entries[0].Name).To(Equal("example ?#.sqlite"))
		link := filepath.Join(dir, "linked")
		Expect(os.Symlink(dir, link)).To(Succeed())
		listing, err = list(link)
		Expect(err).NotTo(HaveOccurred())
		Expect(listing.Path).To(Equal(link))
	})
	It("reports missing paths instead of listing another directory", func() {
		_, err := list(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
	It("lists directories and files without returning contents", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "example.db"), []byte("private contents"), 0600)).To(Succeed())
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/connections/files?path="+dir, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		Handler("/api", http.NotFoundHandler()).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring("example.db"))
		Expect(rec.Body.String()).NotTo(ContainSubstring("private contents"))
	})
	DescribeTable("refuses nonlocal and cross-site requests", func(host, peer, origin, site string) {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/connections/files", nil)
		req.RemoteAddr = peer
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", site)
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		rec := httptest.NewRecorder()
		Handler("/api", http.NotFoundHandler()).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusForbidden))
	}, Entry("remote peer", "localhost", "192.0.2.1:1234", "", ""),
		Entry("rebound host", "example.com", "127.0.0.1:1234", "", ""),
		Entry("cross-site origin", "localhost", "127.0.0.1:1234", "https://example.com", ""),
		Entry("cross-site metadata", "localhost", "127.0.0.1:1234", "", "cross-site"))
})
