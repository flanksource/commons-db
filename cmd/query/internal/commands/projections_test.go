package commands_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Projections CLI", func() {
	ginkgo.It("keeps local exec operations out of the served command tree", func() {
		dir := ginkgo.GinkgoT().TempDir()
		root, err := commands.New(commands.Options{Args: []string{"--config-dir", dir, "--profiles-dir", filepath.Join(dir, "profiles"), "--db", "", "serve"}, Stdout: io.Discard, Stderr: io.Discard, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
		Expect(err).NotTo(HaveOccurred())
		command, _, _ := root.Find([]string{"projections"})
		Expect(command.Name()).NotTo(Equal("projections"))
	})
	for _, plain := range []bool{true, false} {
		ginkgo.It("renders the complete syntax reference with optional color", func() {
			ginkgo.GinkgoT().Setenv("NO_COLOR", "")
			ginkgo.GinkgoT().Setenv("COLOR", "")
			ginkgo.GinkgoT().Setenv("TERM", "xterm-256color")
			ginkgo.GinkgoT().Setenv("CLICKY_FORCE_INTERACTIVE", "1")
			args := []string{"projections", "syntax", "--help"}
			if plain {
				args = append(args, "--no-color")
			}
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: args, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			Expect(output.String()).To(ContainSubstring("perTarget"))
			Expect(output.String()).To(ContainSubstring("XDG_CONFIG_HOME"))
			Expect(output.String()).To(ContainSubstring("faro projection run"))
			if plain {
				Expect(output.String()).NotTo(ContainSubstring("\x1b["))
			} else {
				Expect(output.String()).To(ContainSubstring("\x1b["))
			}
		})
	}
	for _, command := range []string{"verify", "run", "apply", "syntax"} {
		ginkgo.It("provides "+command+" without loading invalid profiles or a database", func() {
			dir := ginkgo.GinkgoT().TempDir()
			profiles := filepath.Join(dir, "profiles")
			Expect(os.MkdirAll(profiles, 0700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(profiles, "broken.yaml"), []byte("[invalid"), 0600)).To(Succeed())
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: []string{"--profiles-dir", profiles, "--db", "invalid", "projections", command, "--help"}, Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			Expect(output.String()).To(ContainSubstring("query projections " + command))
		})
	}
})
