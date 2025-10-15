package e2e

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/e2e/helpers"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Commons-DB E2E Suite")
}

var (
	serviceManager *helpers.ServiceManager
	dockerManager  *helpers.DockerManager
	ctx            context.Context
)

var _ = BeforeSuite(func() {
	GinkgoWriter.Println("Starting Commons-DB E2E test suite")

	ctx = context.Background()

	// Initialize service managers
	serviceManager = helpers.NewServiceManager()
	dockerManager = helpers.NewDockerManager()

	// Start all native services
	GinkgoWriter.Println("Starting native services (Postgres, Redis, OpenSearch, Loki, LocalStack)...")
	Expect(serviceManager.StartAll(ctx)).To(Succeed())

	// Start Docker containers
	GinkgoWriter.Println("Starting Docker containers (SFTP, SMB, GCS, Azurite)...")
	Expect(dockerManager.StartAll(ctx)).To(Succeed())

	// Wait for all services to be healthy
	GinkgoWriter.Println("Waiting for all services to become healthy...")
	Eventually(func() bool {
		return serviceManager.AllHealthy() && dockerManager.AllHealthy()
	}, "2m", "5s").Should(BeTrue())

	GinkgoWriter.Println("All services ready")
})

var _ = AfterSuite(func() {
	GinkgoWriter.Println("Cleaning up E2E test suite")

	if dockerManager != nil {
		Expect(dockerManager.StopAll(ctx)).To(Succeed())
	}

	if serviceManager != nil {
		Expect(serviceManager.StopAll(ctx)).To(Succeed())
	}

	GinkgoWriter.Println("Cleanup complete")
})
