package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

const (
	containerDefaultRoot = "https://container.googleapis.com"
	containerAPIPath     = "/v1"
)

// ContainerClient talks to the Kubernetes Engine (GKE) REST API.
type ContainerClient struct{ *api }

// NewContainerClient builds a GKE client.
func NewContainerClient(opts Options) (*ContainerClient, error) {
	client, err := newAPI(opts, containerDefaultRoot, containerAPIPath)
	if err != nil {
		return nil, err
	}
	return &ContainerClient{api: client}, nil
}

// Cluster is the subset of a GKE cluster commons-db reads.
type Cluster struct {
	// Endpoint is the cluster's control-plane IP address.
	Endpoint   string `json:"endpoint"`
	MasterAuth struct {
		// ClusterCaCertificate is the base64-encoded PEM cluster CA.
		ClusterCaCertificate string `json:"clusterCaCertificate"`
	} `json:"masterAuth"`
}

// ClusterRef names a GKE cluster; Location is a zone or region.
type ClusterRef struct {
	Project  string
	Location string
	Cluster  string
}

// GetCluster fetches a cluster.
func (c *ContainerClient) GetCluster(ctx context.Context, ref ClusterRef) (*Cluster, error) {
	if ref.Project == "" || ref.Location == "" || ref.Cluster == "" {
		return nil, fmt.Errorf("gcp: cluster needs project, location and name, got %+v", ref)
	}
	path := fmt.Sprintf("/projects/%s/locations/%s/clusters/%s",
		url.PathEscape(ref.Project), url.PathEscape(ref.Location), url.PathEscape(ref.Cluster))
	var cluster Cluster
	if err := c.do(ctx, call{Method: http.MethodGet, URL: c.base + path, Out: &cluster}); err != nil {
		return nil, err
	}
	return &cluster, nil
}
