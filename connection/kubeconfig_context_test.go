package connection

import (
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var _ = Describe("Selected Kubernetes connection", func() {
	DescribeTable("does not interpret certificate content as a server file path", func(context string) {
		file := filepath.Join(GinkgoT().TempDir(), "config")
		Expect(clientcmd.WriteToFile(api.Config{CurrentContext: "selected", Clusters: map[string]*api.Cluster{"cluster": {Server: "https://cluster.example.test"}}, Contexts: map[string]*api.Context{"selected": {Cluster: "cluster"}}}, file)).To(Succeed())
		_, _, err := (KubeconfigConnection{Kubeconfig: &types.EnvVar{ValueStatic: file}, Kubecontext: context}).Populate(dbcontext.New())
		Expect(err).To(HaveOccurred())
	}, Entry("current context", ""), Entry("named context", "selected"))

	It("uses saved file/context properties and pins command credentials to the same server", func() {
		file := filepath.Join(GinkgoT().TempDir(), "config")
		raw := api.Config{CurrentContext: "first", Clusters: map[string]*api.Cluster{"one": {Server: "https://one.example.test"}, "two": {Server: "https://two.example.test"}}, Contexts: map[string]*api.Context{"first": {Cluster: "one"}, "second": {Cluster: "two"}}}
		Expect(clientcmd.WriteToFile(raw, file)).To(Succeed())
		model := models.Connection{Name: "cluster", Type: models.ConnectionTypeKubernetes, Properties: map[string]string{"kubeconfig": file, "kubecontext": "second"}}
		ctx := dbcontext.New().WithConnectionResolver(func(dbcontext.Context, string) (*models.Connection, error) { return &model, nil })
		conn := KubernetesConnection{KubeconfigConnection: KubeconfigConnection{ConnectionName: "connection://cluster"}}
		_, config, err := conn.Populate(ctx, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Host).To(Equal("https://two.example.test"))
		cmd := exec.Command("true")
		cmd.Dir = GinkgoT().TempDir()
		result, err := SetupConnection(ctx, ExecConnections{Kubernetes: &conn}, cmd)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(result.Cleanup)
		Expect(result.ApiServer).To(Equal(config.Host))
		for _, env := range cmd.Env {
			if path, ok := strings.CutPrefix(env, "KUBECONFIG="); ok {
				data, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())
				client, err := clientcmd.NewClientConfigFromBytes(data)
				Expect(err).NotTo(HaveOccurred())
				effective, err := client.ClientConfig()
				Expect(err).NotTo(HaveOccurred())
				Expect(effective.Host).To(Equal(config.Host))
			}
		}
	})
})
