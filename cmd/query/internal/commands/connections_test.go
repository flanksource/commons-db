package commands_test

import (
	"io"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("file-first connections", func() {
	It("manages files without loading profiles or opening the configured database", func() {
		base := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", base)
		badProfiles := filepath.Join(base, "bad-profiles")
		Expect(os.WriteFile(badProfiles, []byte("invalid"), 0600)).To(Succeed())
		run := func(args ...string) error {
			root, err := commands.New(commands.Options{Args: append([]string{"--profiles-dir", badProfiles, "--db", "invalid", "connections"}, args...), Stdout: io.Discard, Stderr: io.Discard, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "test", Date: "test"}})
			if err != nil {
				return err
			}
			return root.Execute()
		}
		Expect(run("add", "sqlite", "--name", "local", "--url", "file:example.db")).To(Succeed())
		path := filepath.Join(base, "flanksource", "connections", "local.yaml")
		Expect(path).To(BeAnExistingFile())
		Expect(run("list")).To(Succeed())
		Expect(run("edit", "local", "--url", "file:changed.db", "--insecure-tls=false")).To(Succeed())
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("file:changed.db"))
		Expect(run("add", "sqlite", "--name", "local", "--url", "file:other.db")).NotTo(Succeed())
		Expect(run("validate", "local")).NotTo(Succeed())
		Expect("changed.db").NotTo(BeAnExistingFile())
		Expect(run("delete", "local")).To(Succeed())
		Expect(path).NotTo(BeAnExistingFile())
		Expect(run("list", "--source=unknown")).NotTo(Succeed())
	})
})
