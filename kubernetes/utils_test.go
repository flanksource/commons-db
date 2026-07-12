package kubernetes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
)

func TestGetAPIServer(t *testing.T) {
	tests := []struct {
		name           string
		kubeconfigPath string
		expected       string
	}{
		{
			name:           "valid kubeconfig",
			kubeconfigPath: "kubeconfig.yaml",
			expected:       "https://10.99.99.222:6443",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)

			f, err := os.ReadFile(filepath.Join("testdata", tc.kubeconfigPath))
			g.Expect(err).To(gomega.BeNil())

			result, err := GetAPIServer(f)
			g.Expect(err).To(gomega.BeNil())
			g.Expect(result).To(gomega.Equal(tc.expected))
		})
	}
}

func TestRestConfigFingerprintScopesCredentialsWithoutExposingThem(t *testing.T) {
	secret := "bearer-secret"
	base := &rest.Config{Host: "https://cluster", BearerToken: secret, TLSClientConfig: rest.TLSClientConfig{CAData: []byte("ca-one")}}
	got := RestConfigFingerprint(base)
	if len(got) != 64 || strings.Contains(got, secret) {
		t.Fatalf("unsafe fingerprint %q", got)
	}
	otherPrincipal := rest.CopyConfig(base)
	otherPrincipal.BearerToken = "other"
	if got == RestConfigFingerprint(otherPrincipal) {
		t.Fatal("different principals shared a fingerprint")
	}
	otherCA := rest.CopyConfig(base)
	otherCA.TLSClientConfig.CAData = []byte("ca-two")
	if got == RestConfigFingerprint(otherCA) {
		t.Fatal("different TLS trust roots shared a fingerprint")
	}
}
