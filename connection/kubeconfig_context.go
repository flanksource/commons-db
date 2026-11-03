package connection

import (
	"os"

	dbkubernetes "github.com/flanksource/commons-db/kubernetes"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

func kubernetesClient(options dbkubernetes.Options) (kubernetes.Interface, *rest.Config, error) {
	config, err := options.RESTConfig()
	if err != nil {
		return nil, nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	return client, config, err
}

func kubeconfigBytes(config *rest.Config) ([]byte, error) {
	return clientcmd.Write(api.Config{
		CurrentContext: "connection",
		Contexts:       map[string]*api.Context{"connection": {Cluster: "connection", AuthInfo: "connection"}},
		Clusters:       map[string]*api.Cluster{"connection": {Server: config.Host, CertificateAuthority: config.CAFile, CertificateAuthorityData: config.CAData, InsecureSkipTLSVerify: config.Insecure, TLSServerName: config.ServerName}},
		AuthInfos:      map[string]*api.AuthInfo{"connection": {Token: config.BearerToken, TokenFile: config.BearerTokenFile, Username: config.Username, Password: config.Password, ClientCertificate: config.CertFile, ClientCertificateData: config.CertData, ClientKey: config.KeyFile, ClientKeyData: config.KeyData, Exec: config.ExecProvider, AuthProvider: config.AuthProvider, Impersonate: config.Impersonate.UserName, ImpersonateUID: config.Impersonate.UID, ImpersonateGroups: config.Impersonate.Groups, ImpersonateUserExtra: config.Impersonate.Extra}},
	})
}

func kubeconfigContextClient(value, context string) (kubernetes.Interface, *rest.Config, error) {
	if _, err := os.Stat(value); err == nil {
		return kubernetesClient(dbkubernetes.Options{Kubeconfig: value, Kubecontext: context})
	}
	raw, err := clientcmd.Load([]byte(value))
	if err != nil {
		return nil, nil, err
	}
	config, err := clientcmd.NewDefaultClientConfig(*raw, &clientcmd.ConfigOverrides{CurrentContext: context}).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	return client, config, err
}
