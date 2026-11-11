package sqlite_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLiteConsumer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Standalone SQLite consumer")
}

var _ = Describe("SQLite dependency isolation", func() {
	It("runs GORM and modernc functions without a downstream SQLite replacement", func() {
		root, err := filepath.Abs("../..")
		Expect(err).NotTo(HaveOccurred())
		dir := GinkgoT().TempDir()
		Expect(os.CopyFS(dir, os.DirFS("testdata/consumer"))).To(Succeed())
		command := func(args ...string) *exec.Cmd {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
			return cmd
		}
		output, err := command("mod", "edit", "-replace=github.com/flanksource/commons-db="+root).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
		output, err = command("run", "-mod=mod", ".").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
		Expect(string(output)).To(ContainSubstring("sqlite consumer passed"))
		output, err = command("list", "-deps", ".").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
		Expect(string(output)).NotTo(ContainSubstring("github.com/glebarez/"))
	})
})
