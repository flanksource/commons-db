package connection

import (
	"encoding/base64"
	"fmt"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/context"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// +kubebuilder:object:generate=true
type GKEConnection struct {
	GCPConnection `json:",inline" yaml:",inline"`

	ProjectID string `json:"projectID" yaml:"projectID"`
	Zone      string `json:"zone"`
	Cluster   string `json:"cluster"`
}

func (t *GKEConnection) Populate(ctx ConnectionContext) error {
	return t.GCPConnection.HydrateConnection(ctx)
}

func (t *GKEConnection) Validate() *GKEConnection {
	if t == nil {
		return &GKEConnection{}
	}

	return t
}

// Client builds the Kubernetes Engine API client for the connection. A
// connection with no credentials is unauthenticated.
func (t *GKEConnection) Client(ctx context.Context) (*gcp.ContainerClient, error) {
	t = t.Validate()

	credential := ""
	if t.Credentials != nil && !t.Credentials.IsEmpty() {
		var err error
		if credential, err = ctx.GetEnvValueFromCache(*t.Credentials, ctx.GetNamespace()); err != nil {
			return nil, err
		}
	}

	opts, err := t.ClientOptions(ctx, credential, gcp.ScopeCloudPlatform)
	if err != nil {
		return nil, err
	}
	return gcp.NewContainerClient(opts)
}

func (t *GKEConnection) KubernetesClient(ctx context.Context, freshToken bool) (kubernetes.Interface, *rest.Config, error) {
	containerClient, err := t.Client(ctx)
	if err != nil {
		return nil, nil, err
	}

	cluster, err := containerClient.GetCluster(ctx, gcp.ClusterRef{Project: t.ProjectID, Location: t.Zone, Cluster: t.Cluster})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	token, err := t.GCPConnection.Token(ctx, freshToken, gcp.ScopeCloudPlatform)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token for gke: %w", err)
	}

	ca, err := base64.URLEncoding.DecodeString(cluster.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to decode cluster CA certificate: %w", err)
	}

	restConfig := &rest.Config{
		Host:        "https://" + cluster.Endpoint,
		BearerToken: token.AccessToken,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: ca,
		},
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, err
	}

	return clientset, restConfig, nil
}
