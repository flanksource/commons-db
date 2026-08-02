package migrate

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestProvisioner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Migration Provisioner Suite")
}
