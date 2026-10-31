package connection

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKubernetesConnectionDecodesKubeconfigFieldsFromStrictYAML(t *testing.T) {
	const (
		connectionName = "connection://kubernetes/acme"
		kubeconfig     = "apiVersion: v1\nkind: Config\n"
	)
	source := []byte("kubernetes:\n  connection: " + connectionName + "\n  kubeconfig: {value: " + `"apiVersion: v1\nkind: Config\n"` + "}\n")
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)

	var connections ExecConnections
	if err := decoder.Decode(&connections); err != nil {
		t.Fatal(err)
	}

	got := connections.Kubernetes
	if got == nil || got.ConnectionName != connectionName || got.Kubeconfig == nil || got.Kubeconfig.ValueStatic != kubeconfig {
		t.Fatalf("decoded kubernetes connection = %+v", got)
	}
}

func TestKubernetesConnectionDecodesCNRMFieldsFromStrictYAML(t *testing.T) {
	const (
		projectID       = "acme-project"
		clusterResource = "acme-cluster"
		namespace       = "acme-ns"
	)
	source := []byte("kubernetes:\n  cnrm:\n    gke: {projectID: " + projectID + "}\n    clusterResource: " + clusterResource + "\n    clusterResourceNamespace: " + namespace + "\n")
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)

	var connections ExecConnections
	if err := decoder.Decode(&connections); err != nil {
		t.Fatal(err)
	}

	cnrm := connections.Kubernetes.CNRM
	if cnrm == nil || cnrm.GKE.ProjectID != projectID || cnrm.ClusterResource != clusterResource || cnrm.ClusterResourceNamespace != namespace {
		t.Fatalf("decoded cnrm connection = %+v", cnrm)
	}
}
