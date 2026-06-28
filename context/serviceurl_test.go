package context

import (
	"context"

	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

var _ = ginkgo.Describe("parseServiceRef", func() {
	type want struct {
		ok                            bool
		strategy, name, ns, port, pth string
		kind, selector                string
	}
	cases := []struct {
		raw  string
		want want
	}{
		{"svc://db.prod:5432", want{ok: true, strategy: "svc", name: "db", ns: "prod", port: "5432"}},
		{"svc://db:5432", want{ok: true, strategy: "svc", name: "db", port: "5432"}},
		{"ip://db.prod:5432", want{ok: true, strategy: "ip", name: "db", ns: "prod", port: "5432"}},
		{"proxy://app.prod:8080/PASJava", want{ok: true, strategy: "proxy", name: "app", ns: "prod", port: "8080", pth: "/PASJava"}},
		{"host://ingress.prod", want{ok: true, strategy: "host", name: "ingress", ns: "prod"}},
		{"portforward://db.prod:5432", want{ok: true, strategy: "portforward", name: "db", ns: "prod", port: "5432"}},
		{"portforward://db.prod:5432?kind=service", want{ok: true, strategy: "portforward", name: "db", ns: "prod", port: "5432", kind: "service"}},
		{"portforward://api.prod:8080?kind=deployment", want{ok: true, strategy: "portforward", name: "api", ns: "prod", port: "8080", kind: "deployment"}},
		{"portforward://.prod:5432?selector=app%3Ddb", want{ok: true, strategy: "portforward", ns: "prod", port: "5432", selector: "app=db"}},
		{"portforward://.prod:5432", want{ok: false}}, // no name and no selector
		{"postgres://user:pass@host:5432/db", want{ok: false}},
		{"http://prometheus:9090", want{ok: false}},
		{"user:pass@tcp(host:3306)/db", want{ok: false}},
		{"", want{ok: false}},
	}

	for _, tc := range cases {
		tc := tc
		ginkgo.It("parses "+tc.raw, func() {
			ref, ok := parseServiceRef(tc.raw)
			Expect(ok).To(Equal(tc.want.ok))
			if !tc.want.ok {
				return
			}
			Expect(ref.strategy).To(Equal(tc.want.strategy))
			Expect(ref.name).To(Equal(tc.want.name))
			Expect(ref.namespace).To(Equal(tc.want.ns))
			Expect(ref.port).To(Equal(tc.want.port))
			Expect(ref.path).To(Equal(tc.want.pth))
			Expect(ref.kind).To(Equal(tc.want.kind))
			Expect(ref.selector).To(Equal(tc.want.selector))
		})
	}
})

var _ = ginkgo.Describe("expandServiceURL", func() {
	newCtx := func() Context {
		clientset := fake.NewSimpleClientset(
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.5"},
			},
			&networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "app.example.com"}},
				},
			},
			&networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "secure", Namespace: "prod"},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "secure.example.com"}},
					TLS:   []networkingv1.IngressTLS{{Hosts: []string{"secure.example.com"}}},
				},
			},
		)
		client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{Host: "https://api.k8s.local"})
		return Context{Context: commons.NewContext(context.Background())}.WithLocalKubernetes(client)
	}

	ginkgo.It("svc:// builds in-cluster DNS with the type's scheme", func() {
		got, err := newCtx().expandServiceURL("svc://db.prod:5432", models.ConnectionTypePostgres, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("postgres://db.prod.svc.cluster.local:5432"))
	})

	ginkgo.It("svc:// falls back to the connection namespace when omitted", func() {
		got, err := newCtx().expandServiceURL("svc://db:8080/api", models.ConnectionTypeHTTP, "prod")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("http://db.prod.svc.cluster.local:8080/api"))
	})

	ginkgo.It("ip:// resolves the service cluster IP", func() {
		got, err := newCtx().expandServiceURL("ip://db.prod:5432", models.ConnectionTypePostgres, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("postgres://10.0.0.5:5432"))
	})

	ginkgo.It("host:// resolves the first ingress host (http when no TLS)", func() {
		got, err := newCtx().expandServiceURL("host://web.prod", models.ConnectionTypeHTTP, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("http://app.example.com"))
	})

	ginkgo.It("host:// upgrades to https when the ingress terminates TLS for the host", func() {
		got, err := newCtx().expandServiceURL("host://secure.prod", models.ConnectionTypeHTTP, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("https://secure.example.com"))
	})

	ginkgo.It("host:// rejects non-HTTP connection types", func() {
		_, err := newCtx().expandServiceURL("host://web.prod", models.ConnectionTypePostgres, "")
		Expect(err).To(HaveOccurred())
	})

	ginkgo.It("proxy:// builds the apiserver service-proxy URL for HTTP", func() {
		got, err := newCtx().expandServiceURL("proxy://app.prod:8080/PASJava", models.ConnectionTypeHTTP, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("https://api.k8s.local/api/v1/namespaces/prod/services/http:app:8080/proxy/PASJava"))
	})

	ginkgo.It("proxy:// rejects non-HTTP connection types", func() {
		_, err := newCtx().expandServiceURL("proxy://db.prod:5432", models.ConnectionTypePostgres, "")
		Expect(err).To(HaveOccurred())
	})

	ginkgo.It("leaves plain DSNs untouched", func() {
		got, err := newCtx().expandServiceURL("postgres://user:pass@host:5432/db", models.ConnectionTypePostgres, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal("postgres://user:pass@host:5432/db"))
	})
})
