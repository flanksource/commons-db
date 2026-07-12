package connection

import (
	"strings"
	"testing"

	"github.com/flanksource/commons-db/types"
)

func TestKubernetesConnectionHashDoesNotExposeCredentials(t *testing.T) {
	secret := "super-secret-kubeconfig-token"
	connection := KubernetesConnection{KubeconfigConnection: KubeconfigConnection{
		Kubeconfig: &types.EnvVar{ValueStatic: "apiVersion: v1\ntoken: " + secret},
	}}
	got := connection.Hash()
	if strings.Contains(got, secret) || strings.Contains(got, "apiVersion") {
		t.Fatalf("hash exposed kubeconfig material: %q", got)
	}
	if len(got) != 64 {
		t.Fatalf("hash length = %d, want SHA-256 hex", len(got))
	}
	if got != connection.Hash() {
		t.Fatal("hash is not stable")
	}

	other := connection
	other.Kubeconfig = &types.EnvVar{ValueStatic: "apiVersion: v1\ntoken: another"}
	if got == other.Hash() {
		t.Fatal("different credentials must not share a client cache identity")
	}
}

func TestCNRMKubernetesConnectionHashDoesNotPanic(t *testing.T) {
	connection := KubernetesConnection{CNRM: &CNRMConnection{ClusterResource: "prod"}}
	if got := connection.Hash(); len(got) != 64 {
		t.Fatalf("CNRM hash = %q", got)
	}
}
