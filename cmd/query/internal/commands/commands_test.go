package commands_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	build := commands.BuildInfo{Version: "dev", Commit: "unknown", Date: "unknown"}

	It("composes application commands and generated domain commands", func() {
		configDir := filepath.Join(GinkgoT().TempDir(), "config")
		profilesDir := filepath.Join(GinkgoT().TempDir(), "profiles")
		root, err := commands.New(commands.Options{
			Args:   []string{"--config-dir", configDir, "--profiles-dir", profilesDir},
			Stdout: io.Discard, Stderr: io.Discard, BuildInfo: build,
		})
		Expect(err).NotTo(HaveOccurred())

		for _, path := range []string{"version", "serve", "schema", "trace", "top", "connection", "profiles"} {
			command, _, err := root.Find([]string{path})
			Expect(err).NotTo(HaveOccurred(), path)
			Expect(command.Name()).To(Equal(path))
		}
		Expect(root.PersistentFlags().Lookup("config-dir").DefValue).To(Equal(configDir))
		Expect(root.PersistentFlags().Lookup("profiles-dir").DefValue).To(Equal(profilesDir))
		// --db and --data-dir are persistent so `serve` and every sub-command name
		// the same store; serve must not shadow --data-dir with its own copy.
		Expect(root.PersistentFlags().Lookup("db").DefValue).To(Equal("embedded"))
		Expect(root.PersistentFlags().Lookup("data-dir").DefValue).To(Equal(filepath.Join(configDir, "postgres")))
		serve, _, err := root.Find([]string{"serve"})
		Expect(err).NotTo(HaveOccurred())
		Expect(serve.Flags().Lookup("data-dir")).To(BeNil())
		Expect(serve.Flags().Lookup("reconcile-snapshot-max-age").DefValue).To(Equal("1h0m0s"))
	})

	DescribeTable("builds the command tree without starting postgres",
		func(args []string) {
			configDir := filepath.Join(GinkgoT().TempDir(), "config")
			root, err := commands.New(commands.Options{
				Args: append([]string{
					"--config-dir", configDir,
					"--profiles-dir", filepath.Join(GinkgoT().TempDir(), "profiles"),
				}, args...),
				Stdout: io.Discard, Stderr: io.Discard, BuildInfo: build,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(root).NotTo(BeNil())

			// The embedded cluster lives at <config-dir>/postgres. Its absence is
			// what proves a metadata-only invocation never opened the database.
			Expect(filepath.Join(configDir, "postgres")).NotTo(BeAnExistingFile())
		},
		Entry("with no command", []string{}),
		Entry("with --help", []string{"--help"}),
		Entry("with -h", []string{"-h"}),
		Entry("from the help command", []string{"help"}),
		Entry("from the schema command", []string{"schema", "--out", "/dev/null"}),
		Entry("from shell completion", []string{"__complete", ""}),
	)

	It("falls back to the file store when --db is explicitly empty", func() {
		configDir := filepath.Join(GinkgoT().TempDir(), "config")
		root, err := commands.New(commands.Options{
			Args: []string{
				"--config-dir", configDir,
				"--profiles-dir", filepath.Join(GinkgoT().TempDir(), "profiles"),
				"--db=", "profiles",
			},
			Stdout: io.Discard, Stderr: io.Discard, BuildInfo: build,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(root).NotTo(BeNil())
		Expect(filepath.Join(configDir, "postgres")).NotTo(BeAnExistingFile())
	})

	DescribeTable("prints the injected build information without initializing application state",
		func(invocation string) {
			stdout := new(bytes.Buffer)
			build := commands.BuildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-08-09T10:30:00Z"}
			profilesDir := filepath.Join(GinkgoT().TempDir(), "profiles")
			Expect(os.WriteFile(profilesDir, []byte("not a directory"), 0o600)).To(Succeed())
			root, err := commands.New(commands.Options{
				Args: []string{
					"--config-dir", filepath.Join(GinkgoT().TempDir(), "config"),
					"--profiles-dir", profilesDir,
					invocation,
				},
				Stdout: stdout, Stderr: io.Discard, BuildInfo: build,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(root.Execute()).To(Succeed())
			Expect(stdout.String()).To(Equal(fmt.Sprintf(
				"query version %s (commit: %s, built: %s, go: %s)\n",
				build.Version, build.Commit, build.Date, runtime.Version(),
			)))
		},
		Entry("from the version command", "version"),
		Entry("from the version flag", "--version"),
	)

	DescribeTable("rejects incomplete build information",
		func(build commands.BuildInfo, message string) {
			_, err := commands.New(commands.Options{Stdout: io.Discard, Stderr: io.Discard, BuildInfo: build})
			Expect(err).To(MatchError(message))
		},
		Entry("without a version", commands.BuildInfo{Commit: "abc1234", Date: "2026-08-09T10:30:00Z"}, "query build version is required"),
		Entry("without a commit", commands.BuildInfo{Version: "v1.2.3", Date: "2026-08-09T10:30:00Z"}, "query build commit is required"),
		Entry("without a build date", commands.BuildInfo{Version: "v1.2.3", Commit: "abc1234"}, "query build date is required"),
	)

	It("exposes the profile replay and reconcile actions with their flags", func() {
		root, err := commands.New(commands.Options{
			Args: []string{
				"--config-dir", filepath.Join(GinkgoT().TempDir(), "config"),
				"--profiles-dir", filepath.Join(GinkgoT().TempDir(), "profiles"),
			},
			Stdout: io.Discard, Stderr: io.Discard, BuildInfo: build,
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
		for _, flag := range []string{"dest", "key-cel", "key-columns", "time-column", "source-filter", "dest-filter", "snapshot-age"} {
			Expect(reconcile.Flags().Lookup(flag)).NotTo(BeNil(), flag)
		}
	})
})
