package profiles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("profile OpenAPI representations", func() {
	var store *FileStore
	var handler http.Handler

	profilePath := "/api/v1/profile/profile-live-sales"

	get := func(accept, ifNoneMatch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil)
		if accept != "" {
			request.Header.Set("Accept", accept)
		}
		if ifNoneMatch != "" {
			request.Header.Set("If-None-Match", ifNoneMatch)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	// The operation as the wire carries it, so a stubbed `responses` is read as
	// the client reads it rather than through the Go types that produced it.
	operation := func(response *httptest.ResponseRecorder, path string) map[string]any {
		var document struct {
			Paths map[string]map[string]map[string]any `json:"paths"`
		}
		Expect(json.Unmarshal(response.Body.Bytes(), &document)).To(Succeed())
		Expect(document.Paths).To(HaveKey(path))
		return document.Paths[path]["get"]
	}

	BeforeEach(func() {
		var err error
		store, err = NewFileStore(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Save(context.Background(), sampleProfile("Live Sales"))).To(Succeed())
		handler = newProfileOpenAPIHandler(&cobra.Command{Use: "query"}, &rpc.Config{}, store, nil)
	})

	It("stubs the response schemas the catalog asks for and keeps the rest", func() {
		catalog := get(clickyOpenAPIMediaType, "")
		Expect(catalog.Code).To(Equal(http.StatusOK))
		Expect(catalog.Header().Get("Content-Type")).To(Equal(clickyOpenAPIMediaType))
		Expect(catalog.Header().Values("Vary")).To(ContainElement("Accept"))

		stubbed := operation(catalog, profilePath)
		// Stubbed, not dropped: an operation without responses is not a valid
		// OpenAPI document, and `null` is not a stub.
		Expect(stubbed["responses"]).To(Equal(map[string]any{
			"200": map[string]any{"description": "OK"},
		}))
		Expect(stubbed["parameters"]).NotTo(BeEmpty())
		Expect(stubbed).To(HaveKey("x-clicky"))

		complete := get("application/json", "")
		Expect(complete.Header().Get("Content-Type")).To(Equal("application/json"))
		full := operation(complete, profilePath)
		Expect(full["responses"]).NotTo(Equal(stubbed["responses"]))
		// Only the responses differ — the catalog is the same document, and the
		// bytes it saves are the point of serving it.
		Expect(full["parameters"]).To(Equal(stubbed["parameters"]))
		Expect(catalog.Body.Len()).To(BeNumerically("<", complete.Body.Len()))
	})

	It("revalidates each representation against its own tag", func() {
		full := get("application/json", "")
		catalog := get(clickyOpenAPIMediaType, "")
		Expect(full.Header().Get("ETag")).NotTo(BeEmpty())
		Expect(full.Header().Get("Cache-Control")).To(Equal("no-cache"))
		Expect(catalog.Header().Get("ETag")).NotTo(Equal(full.Header().Get("ETag")))

		repeated := get("application/json", "")
		Expect(repeated.Header().Get("ETag")).To(Equal(full.Header().Get("ETag")))

		notModified := get("application/json", full.Header().Get("ETag"))
		Expect(notModified.Code).To(Equal(http.StatusNotModified))
		Expect(notModified.Body.Len()).To(BeZero())

		// A tag names one representation's bytes: the other must not match it.
		crossed := get("application/json", catalog.Header().Get("ETag"))
		Expect(crossed.Code).To(Equal(http.StatusOK))
	})

	It("rebuilds when a profile is saved or deleted", func() {
		before := get(clickyOpenAPIMediaType, "")
		Expect(store.Save(context.Background(), sampleProfile("Nightly Report"))).To(Succeed())

		added := get(clickyOpenAPIMediaType, "")
		Expect(added.Header().Get("ETag")).NotTo(Equal(before.Header().Get("ETag")))
		Expect(added.Body.String()).To(ContainSubstring("profile-nightly-report"))

		Expect(store.Delete(context.Background(), "Nightly Report")).To(Succeed())
		removed := get(clickyOpenAPIMediaType, "")
		Expect(removed.Header().Get("ETag")).To(Equal(before.Header().Get("ETag")))
		// The surface and its path go, and so must the filter components it
		// registered — the generator carries one components map across
		// generations, so anything left behind outlives the profile.
		Expect(removed.Body.String()).NotTo(ContainSubstring("profile-nightly-report"))
	})

	It("keeps a deleted profile's filter components out of the document", func() {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
		Expect(mergeStoredProfiles(context.Background(), spec, store)).To(Succeed())
		Expect(profileFilterComponents(spec, "live-sales")).NotTo(BeEmpty())

		Expect(store.Delete(context.Background(), "Live Sales")).To(Succeed())
		Expect(mergeStoredProfiles(context.Background(), spec, store)).To(Succeed())
		Expect(profileFilterComponents(spec, "live-sales")).To(BeEmpty())
	})
})

func profileFilterComponents(spec *rpc.OpenAPISpec, slug string) []string {
	if spec.Components == nil {
		return nil
	}
	var names []string
	for name := range spec.Components.ClickyFilters {
		if strings.HasPrefix(name, "profile-"+slug+"-") {
			names = append(names, name)
		}
	}
	return names
}
