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
	ginkgo.DescribeTable("explains the relevant projection workflow in command help",
		func(name string, sections []string) {
			args := []string{"projections"}
			if name != "" {
				args = append(args, name)
			}
			var output bytes.Buffer
			root, err := commands.New(commands.Options{Args: append(args, "--help", "--no-color"), Stdout: &output, Stderr: &output, BuildInfo: commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Execute()).To(Succeed())
			for _, section := range append(sections, "Selecting manifests:", "Output:", "query projections syntax --help") {
				Expect(output.String()).To(ContainSubstring(section))
			}
			Expect(output.String()).NotTo(ContainSubstring("\x1b["))
			Expect(output.String()).NotTo(ContainSubstring("selected Faro context"))
		},
		ginkgo.Entry("root", "", []string{"Workflow:", "Manifest syntax:", "projections.flanksource.com/v1alpha1", "entries: []"}),
		ginkgo.Entry("run", "run", []string{"Source queries:", "Filtering and validation:", "Results and connections:", "perTarget", "source.body"}),
		ginkgo.Entry("apply", "apply", []string{"Matching and mapping syntax:", "List strategies:", "Creation and aggregation:", "Validation and execution:", "Dry runs and results:", "replaceMatching"}),
		ginkgo.Entry("verify", "verify", []string{"Manifest checks:", "Target checks:", "Results and limits:", "future source rows"}),
	)

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
