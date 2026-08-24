package kubecatalog

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestKubernetesCatalog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes Catalog Suite")
}
