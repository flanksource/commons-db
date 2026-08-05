package commands_test

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCommands(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Query commands")
}

var _ = Describe("New", Ordered, func() {
	It("composes application commands and generated domain commands", func() {
		configDir := filepath.Join(GinkgoT().TempDir(), "config")
		profilesDir := filepath.Join(GinkgoT().TempDir(), "profiles")
		root, err := commands.New(commands.Options{
			Args:   []string{"--config-dir", configDir, "--profiles-dir", profilesDir},
			Stdout: io.Discard, Stderr: io.Discard,
		})
		Expect(err).NotTo(HaveOccurred())

		for _, path := range []string{"serve", "schema", "trace", "top", "connection", "profiles"} {
			command, _, err := root.Find([]string{path})
			Expect(err).NotTo(HaveOccurred(), path)
			Expect(command.Name()).To(Equal(path))
		}
		Expect(root.PersistentFlags().Lookup("config-dir").DefValue).To(Equal(configDir))
		Expect(root.PersistentFlags().Lookup("profiles-dir").DefValue).To(Equal(profilesDir))
	})

	It("exposes the profile replay and reconcile actions with their flags", func() {
		root, err := commands.New(commands.Options{
			Args: []string{
				"--config-dir", filepath.Join(GinkgoT().TempDir(), "config"),
				"--profiles-dir", filepath.Join(GinkgoT().TempDir(), "profiles"),
			},
			Stdout: io.Discard, Stderr: io.Discard,
		})
		Expect(err).NotTo(HaveOccurred())

		replay, _, err := root.Find([]string{"profiles", "replay"})
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.Name()).To(Equal("replay"))
		for _, flag := range []string{"param", "select", "target", "method", "url", "body", "header", "execute", "preview-hash"} {
			Expect(replay.Flags().Lookup(flag)).NotTo(BeNil(), flag)
		}
		// Previewing must stay the default: --execute is what sends the request.
		Expect(replay.Flags().Lookup("execute").DefValue).To(Equal("false"))

		reconcile, _, err := root.Find([]string{"profiles", "reconcile"})
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcile.Name()).To(Equal("reconcile"))
		for _, flag := range []string{"dest", "key-cel", "key-columns", "time-column", "param"} {
			Expect(reconcile.Flags().Lookup(flag)).NotTo(BeNil(), flag)
		}
	})
})
