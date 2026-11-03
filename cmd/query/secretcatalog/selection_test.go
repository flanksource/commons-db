package secretcatalog

import (
	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
)

var _ = Describe("Kubernetes selection discovery", func() {
	It("lists selected file contexts without accessing the ambient cluster", func() {
		file := filepath.Join(GinkgoT().TempDir(), "target.yaml")
		Expect(clientcmd.WriteToFile(api.Config{CurrentContext: "selected", Contexts: map[string]*api.Context{"selected": {Cluster: "target"}}}, file)).To(Succeed())
		handler := NewHandler(Options{Context: dbcontext.New(), Prefix: "/api", Kube: func() (kubernetes.Interface, error) { Fail("ambient client must not be used"); return nil, nil }, Next: http.NotFoundHandler()})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/contexts?kubeconfig="+url.QueryEscape(file), nil))
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(MatchJSON(`{"currentContext":"selected","contexts":["selected"]}`))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/namespaces?kubeconfig="+url.QueryEscape(file+".missing"), nil))
		Expect(response.Code).To(Equal(http.StatusBadRequest))
	})
})
