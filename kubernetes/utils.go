package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/commons/hash"
	"k8s.io/client-go/rest"
	clientcmdAPI "k8s.io/client-go/tools/clientcmd/api/v1"

	// NOTE: must use sigs.k8s.io/yaml instead of gopkg.in/yaml.v2 because it uses json struct tags
	// gopkg.in/yaml will not pickup "current-context"
	"sigs.k8s.io/yaml"
)

// RestConfigFingerprint generates a unique SHA-256 hash to identify the Kubernetes API server
// and client authentication details from the REST configuration.
func RestConfigFingerprint(rc *rest.Config) string {
	if rc == nil {
		return ""
	}

	identity := struct {
		Host, APIPath, Username, Password, BearerToken, BearerTokenFile string
		TLS                                                             rest.TLSClientConfig
		Impersonate                                                     rest.ImpersonationConfig
		AuthProvider                                                    any
		ExecProvider                                                    any
		TransportIdentity                                               string
	}{
		Host: rc.Host, APIPath: rc.APIPath, Username: rc.Username, Password: rc.Password,
		BearerToken: rc.BearerToken, BearerTokenFile: rc.BearerTokenFile,
		TLS: rc.TLSClientConfig, Impersonate: rc.Impersonate,
		AuthProvider: rc.AuthProvider, ExecProvider: rc.ExecProvider,
		TransportIdentity: fmt.Sprintf("proxy=%p|dial=%p|wrap=%p", rc.Proxy, rc.Dial, rc.WrapTransport),
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return hash.Sha256Hex(rc.Host + rc.APIPath + rc.Username)
	}
	return hash.Sha256Hex(string(data))
}

func GetAPIServer(kubeconfigRaw []byte) (string, error) {
	var kubeconfig clientcmdAPI.Config
	if err := yaml.Unmarshal(kubeconfigRaw, &kubeconfig); err != nil {
		return "", err
	}

	var currentCluster string
	for _, c := range kubeconfig.Contexts {
		if c.Name == kubeconfig.CurrentContext {
			currentCluster = c.Context.Cluster
			break
		}
	}

	for _, c := range kubeconfig.Clusters {
		if c.Name == currentCluster {
			return c.Cluster.Server, nil
		}
	}

	return "", errors.New("current cluster not found")
}
