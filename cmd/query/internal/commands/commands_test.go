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
})
