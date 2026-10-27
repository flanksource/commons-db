package context

import (
	"fmt"
	"sync"

	"github.com/flanksource/commons/logger"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
)

// KubeContextProperty is the connection property naming the kubeconfig
// context whose cluster a connection's secret://, configmap:// and helm://
// references are read from.
const KubeContextProperty = "kubecontext"

// KubeContextClients returns the Kubernetes client for a kubeconfig context.
type KubeContextClients func(kubeContext string) (*dutyKubernetes.Client, error)

type kubeContextClientsKey struct{}

// WithKubeContextClients is k building the client for a connection's
// kubecontext property with clients instead of from the default kubeconfig.
func (k Context) WithKubeContextClients(clients KubeContextClients) Context {
	return k.WithValue(kubeContextClientsKey{}, clients)
}

// KubeContextClient is the Kubernetes client for kubeContext: from the
// context's KubeContextClients when it has one, otherwise from the default
// kubeconfig loading rules with kubeContext as the current context.
func (k Context) KubeContextClient(kubeContext string) (*dutyKubernetes.Client, error) {
	if clients, ok := k.Value(kubeContextClientsKey{}).(KubeContextClients); ok && clients != nil {
		return clients(kubeContext)
	}
	return kubeconfigContextClient(kubeContext)
}

var kubeconfigContextClients sync.Map

// kubeconfigContextClient builds a client for one context of the default
// kubeconfig, once per context; a failure is not cached so a fixed kubeconfig
// applies on the next call.
func kubeconfigContextClient(kubeContext string) (*dutyKubernetes.Client, error) {
	if client, ok := kubeconfigContextClients.Load(kubeContext); ok {
		return client.(*dutyKubernetes.Client), nil
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig context %q: %w", kubeContext, err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client for context %q: %w", kubeContext, err)
	}
	client, _ := kubeconfigContextClients.LoadOrStore(kubeContext, dutyKubernetes.NewKubeClient(logger.GetLogger("k8s."+kubeContext), clientset, config))
	return client.(*dutyKubernetes.Client), nil
}
