package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

func TestSelection(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Kubernetes selection") }

var _ = Describe("Kubernetes file and context selection", func() {
	var file string
	BeforeEach(func() {
		file = filepath.Join(GinkgoT().TempDir(), "config")
		Expect(clientcmd.WriteToFile(api.Config{CurrentContext: "first", Clusters: map[string]*api.Cluster{"one": {Server: "https://one.example.test"}, "two": {Server: "https://two.example.test"}}, Contexts: map[string]*api.Context{"first": {Cluster: "one"}, "second": {Cluster: "two"}}}, file)).To(Succeed())
		GinkgoT().Setenv("KUBECONFIG", file)
		GinkgoT().Setenv("KUBERNETES_SERVICE_HOST", "")
	})
	DescribeTable("selects the requested cluster", func(context, expected string) {
		cfg, err := (Options{Kubeconfig: file, Kubecontext: context}).RESTConfig()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Host).To(Equal(expected))
	}, Entry("file current context", "", "https://one.example.test"), Entry("file with context", "second", "https://two.example.test"))
	It("rejects explicit missing files instead of using ambient configuration", func() {
		_, err := (Options{Kubeconfig: file + ".missing"}).RESTConfig()
		Expect(err).To(HaveOccurred())
	})
	It("rejects unknown contexts", func() {
		_, err := (Options{Kubecontext: "missing"}).RESTConfig()
		Expect(err).To(HaveOccurred())
	})
	It("rejects malformed configuration", func() {
		Expect(os.WriteFile(file, []byte("contexts: [invalid"), 0600)).To(Succeed())
		_, err := (Options{Kubeconfig: file}).RESTConfig()
		Expect(err).To(HaveOccurred())
	})
	It("distinguishes identical context names in different files", func() {
		other := filepath.Join(GinkgoT().TempDir(), "config")
		Expect(clientcmd.WriteToFile(api.Config{CurrentContext: "first", Clusters: map[string]*api.Cluster{"one": {Server: "https://other.example.test"}}, Contexts: map[string]*api.Context{"first": {Cluster: "one"}}}, other)).To(Succeed())
		selected, err := (Options{Kubeconfig: other, Kubecontext: "first"}).RESTConfig()
		Expect(err).NotTo(HaveOccurred())
		Expect(selected.Host).To(Equal("https://other.example.test"))
		ambient, err := (Options{Kubecontext: "first"}).RESTConfig()
		Expect(err).NotTo(HaveOccurred())
		Expect(ambient.Host).To(Equal("https://one.example.test"))
	})
})
