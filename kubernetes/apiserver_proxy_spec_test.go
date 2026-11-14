package kubernetes

import (
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
)

var _ = Describe("APIServerProxyFor", func() {
	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		Expect(err).NotTo(HaveOccurred())
		return u
	}

	It("finds the identity registered for any URL under a proxy base", func() {
		config := &rest.Config{Host: "https://registered.k8s.test", BearerToken: "one"}
		base := "https://registered.k8s.test/api/v1/namespaces/shop/pods/http:orders-0:8080/proxy"
		Expect(RegisterAPIServerProxy(base, config)).To(Succeed())

		proxy, err := APIServerProxyFor(mustParse(base + "/orders/health?probe=1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(proxy).NotTo(BeNil())
		Expect(proxy.Config().BearerToken).To(Equal("one"))

		other, err := APIServerProxyFor(mustParse("https://registered.k8s.test/api/v1/namespaces/shop/pods/http:orders-1:8080/proxy/"))
		Expect(err).NotTo(HaveOccurred())
		Expect(other).To(BeNil())
	})

	It("refuses to guess between two kubeconfig identities for one proxy base", func() {
		base := "https://shared.k8s.test/api/v1/namespaces/shop/services/http:orders:80/proxy"
		Expect(RegisterAPIServerProxy(base, &rest.Config{Host: "https://shared.k8s.test", BearerToken: "a"})).To(Succeed())
		Expect(RegisterAPIServerProxy(base, &rest.Config{Host: "https://shared.k8s.test", BearerToken: "b"})).To(Succeed())

		_, err := APIServerProxyFor(mustParse(base + "/"))
		Expect(err).To(MatchError(ContainSubstring("2 kubeconfig identities")))
	})

	It("rejects registering a URL that is not an apiserver proxy URL", func() {
		Expect(RegisterAPIServerProxy("https://shared.k8s.test/api/v1/namespaces/shop/pods/orders", &rest.Config{})).
			To(MatchError(ContainSubstring("not an apiserver proxy URL")))
	})
})
