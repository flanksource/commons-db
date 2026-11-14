package connection_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/connection"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
)

var _ = Describe("HTTP connections to an apiserver proxy URL", Ordered, func() {
	const apiserverToken = "apiserver-token"
	var proxyURL string

	BeforeAll(func() {
		// The fake apiserver answers only requests carrying the kubeconfig bearer
		// token over TLS verified by the kubeconfig CA.
		apiserver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+apiserverToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-Proxied-Path", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		DeferCleanup(apiserver.Close)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: apiserver.Certificate().Raw})
		base := apiserver.URL + "/api/v1/namespaces/shop/pods/http:orders-0:8080/proxy"
		Expect(dutyKubernetes.RegisterAPIServerProxy(base, &rest.Config{
			Host: apiserver.URL, BearerToken: apiserverToken, TLSClientConfig: rest.TLSClientConfig{CAData: ca},
		})).To(Succeed())
		proxyURL = base + "/orders/health"
	})

	It("authenticates Transport() requests with the kubeconfig credentials and CA", func() {
		response, err := (&http.Client{Transport: connection.HTTPConnection{URL: proxyURL}.Transport()}).Get(proxyURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)
		Expect(response.StatusCode).To(Equal(http.StatusNoContent))
		Expect(response.Header.Get("X-Proxied-Path")).To(HaveSuffix("/proxy/orders/health"))
	})

	It("authenticates CreateHTTPClient requests with the kubeconfig credentials and CA", func(ctx SpecContext) {
		client, err := connection.CreateHTTPClient(nil, connection.HTTPConnection{URL: proxyURL})
		Expect(err).NotTo(HaveOccurred())
		response, err := client.R(ctx).Get(proxyURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)
		Expect(response.StatusCode).To(Equal(http.StatusNoContent))
	})

	It("refuses backend credentials the apiserver would strip from the proxied request", func(ctx SpecContext) {
		withBasic := connection.HTTPConnection{URL: proxyURL, HTTPBasicAuth: types.HTTPBasicAuth{Authentication: types.Authentication{
			Username: types.EnvVar{ValueStatic: "manager"}, Password: types.EnvVar{ValueStatic: "secret"},
		}}}
		_, err := (&http.Client{Transport: withBasic.Transport()}).Get(proxyURL)
		Expect(err).To(MatchError(ContainSubstring("deletes the Authorization header")))

		_, err = connection.CreateHTTPClient(nil, connection.HTTPConnection{URL: proxyURL, Bearer: types.EnvVar{ValueStatic: "backend"}})
		Expect(err).To(MatchError(And(ContainSubstring("deletes the Authorization header"), ContainSubstring("query parameter"))))
	})
})
