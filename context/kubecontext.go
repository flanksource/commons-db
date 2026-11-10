package context

import (
	"fmt"
	"sync"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"
	"k8s.io/client-go/kubernetes"
)

const KubeContextProperty = "kubecontext"
const KubeconfigProperty = "kubeconfig"

type KubeContextClients func(options dutyKubernetes.Options) (*dutyKubernetes.Client, error)
type kubeContextClientsKey struct{}

func (k Context) WithKubeContextClients(clients KubeContextClients) Context {
	return k.WithValue(kubeContextClientsKey{}, clients)
}

var kubeconfigContextClients sync.Map

func (k Context) KubeContextClient(options dutyKubernetes.Options) (*dutyKubernetes.Client, error) {
	if clients, ok := k.Value(kubeContextClientsKey{}).(KubeContextClients); ok && clients != nil {
		return clients(options)
	}
	config, err := options.RESTConfig()
	if err != nil {
		return nil, err
	}
	key := dutyKubernetes.RestConfigFingerprint(config)
	if client, ok := kubeconfigContextClients.Load(key); ok {
		return client.(*dutyKubernetes.Client), nil
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	client, _ := kubeconfigContextClients.LoadOrStore(key, dutyKubernetes.NewKubeClient(logger.GetLogger("k8s"), clientset, config))
	return client.(*dutyKubernetes.Client), nil
}

func (k Context) WithConnectionKubernetes(connection *models.Connection) (Context, error) {
	options := dutyKubernetes.Options{Kubeconfig: connection.Properties[KubeconfigProperty], Kubecontext: connection.Properties[KubeContextProperty]}
	if !options.Explicit() {
		return k, nil
	}
	if connection.Type == models.ConnectionTypeKubernetes && options.Kubeconfig == "" && connection.Certificate != "" {
		var certificate types.EnvVar
		if err := certificate.Scan(connection.Certificate); err != nil {
			return k, err
		}
		if certificate.ValueFrom == nil {
			options.Config = []byte(certificate.ValueStatic)
		}
	}
	client, err := k.KubeContextClient(options)
	if err != nil {
		return k, fmt.Errorf("connection %q: %w", connection.Name, err)
	}
	return k.WithLocalKubernetes(client), nil
}
