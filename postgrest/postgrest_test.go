package postgrest

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/api"
)

func TestPostgrest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "postgrest")
}

var _ = Describe("postgrest install", Label("integration"), func() {
	It("downloads the configured PostgREST release and runs it", func() {
		DeferCleanup(os.Chdir, Must(os.Getwd()))
		Expect(os.Chdir(GinkgoT().TempDir())).To(Succeed())

		bin, err := install(api.DefaultConfig)
		Expect(err).NotTo(HaveOccurred())
		Expect(bin).To(Equal(filepath.Join(".bin", "postgrest")))
		Expect(GoOffline()).To(Succeed())
	})
})

func Must[T any](value T, err error) T {
	Expect(err).NotTo(HaveOccurred())
	return value
}
