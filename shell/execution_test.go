package shell

import (
	"bytes"
	stdcontext "context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
)

var _ = Describe("Shell execution", func() {
	It("uses SetupEnv as the exact environment with per-run host passthrough", func() {
		const (
			passthroughKey = "SHELL_TEST_PASSTHROUGH"
			hostOnlyKey    = "SHELL_TEST_HOST_ONLY"
		)
		GinkgoT().Setenv(passthroughKey, "visible")
		GinkgoT().Setenv(hostOnlyKey, "must-not-leak")

		command := exec.Command("sh", "-c",
			`test "$SHELL_TEST_PASSTHROUGH" = visible && test -z "$SHELL_TEST_HOST_ONLY"`)
		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:         GinkgoT().TempDir(),
			BaseDir:        GinkgoT().TempDir(),
			PassthroughEnv: []string{passthroughKey},
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.ExitCode).To(Equal(0))
	})

	It("redacts resolved environment values from capture and live output", func() {
		const sensitiveValue = "configured-sensitive-value"
		var liveStdout, liveStderr bytes.Buffer
		command := exec.Command("sh", "-c", `printf %s "$SHELL_TOKEN"; printf %s "$SHELL_TOKEN" >&2`)
		command.Stdout = &liveStdout
		command.Stderr = &liveStderr

		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			EnvVars: []types.EnvVar{{Name: "SHELL_TOKEN", ValueStatic: sensitiveValue}},
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("[REDACTED]"))
		Expect(details.Stderr).To(Equal("[REDACTED]"))
		Expect(liveStdout.String()).To(Equal("[REDACTED]"))
		Expect(liveStderr.String()).To(Equal("[REDACTED]"))
	})

	It("does not expose resolved environment values in errors or logs", func() {
		const sensitiveValue = "failing-command-sensitive-value"
		var logOutput bytes.Buffer
		log := logger.NewWithWriter(&logOutput)
		log.SetLogLevel(logger.Trace4)
		ctx := dbcontext.New(commons.WithLogger(log))

		details, err := RunCmd(ctx, Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			EnvVars: []types.EnvVar{{Name: "SHELL_TOKEN", ValueStatic: sensitiveValue}},
		}, exec.Command("sh", "-c", `printf %s "$SHELL_TOKEN" >&2; exit 5`))

		Expect(err).To(HaveOccurred())
		Expect(details.Stderr).To(Equal("[REDACTED]"))
		Expect(err.Error()).ToNot(ContainSubstring(sensitiveValue))
		Expect(logOutput.String()).ToNot(ContainSubstring(sensitiveValue))
	})

	It("redacts resolved environment values from file artifacts", func() {
		const sensitiveValue = "artifact-sensitive-value"
		Expect(os.MkdirAll(".tmp", 0o700)).To(Succeed())
		workDir, err := os.MkdirTemp(".tmp", "artifact-")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(os.RemoveAll, workDir)
		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:    workDir,
			BaseDir:   GinkgoT().TempDir(),
			EnvVars:   []types.EnvVar{{Name: "SHELL_TOKEN", ValueStatic: sensitiveValue}},
			Artifacts: []Artifact{{Path: "result.txt"}},
		}, exec.Command("sh", "-c", `printf %s "$SHELL_TOKEN" > result.txt`))

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Artifacts).To(HaveLen(1))
		content, err := io.ReadAll(details.Artifacts[0].Content)
		Expect(err).ToNot(HaveOccurred())
		Expect(details.Artifacts[0].Content.Close()).To(Succeed())
		Expect(string(content)).To(Equal("[REDACTED]"))
	})

	It("redacts a chunked nested connection secret", func() {
		const sensitiveValue = "nested-connection-sensitive-value"
		var liveStdout bytes.Buffer
		command := exec.Command("bash", "-c",
			`value=$(awk '/aws_secret_access_key/{print $3}' "$AWS_SHARED_CREDENTIALS_FILE"); printf %s "${value:0:12}"; sleep 0.05; printf %s "${value:12}"`)
		command.Stdout = &liveStdout

		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			Connections: connection.ExecConnections{AWS: &connection.AWSConnection{
				AccessKey: types.EnvVar{ValueStatic: "nested-access-key"},
				SecretKey: types.EnvVar{ValueStatic: sensitiveValue},
			}},
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("[REDACTED]"))
		Expect(liveStdout.String()).To(Equal("[REDACTED]"))
	})

	It("removes credential files when a later connection setup fails", func() {
		workDir := GinkgoT().TempDir()
		GinkgoT().Setenv("PATH", "")
		setup, err := SetupEnv(shellContext(stdcontext.Background()), &Exec{
			Chroot:  workDir,
			BaseDir: GinkgoT().TempDir(),
			Connections: connection.ExecConnections{
				AWS: &connection.AWSConnection{
					AccessKey: types.EnvVar{ValueStatic: "cleanup-access-key"},
					SecretKey: types.EnvVar{ValueStatic: "cleanup-secret-key"},
				},
				Azure: &connection.AzureConnection{
					ClientID:     &types.EnvVar{ValueStatic: "cleanup-client"},
					ClientSecret: &types.EnvVar{ValueStatic: "cleanup-client-secret"},
					TenantID:     "cleanup-tenant",
				},
			},
		})

		Expect(err).To(HaveOccurred())
		Expect(setup).To(BeNil())
		credentialDirs, globErr := filepath.Glob(filepath.Join(workDir, ".creds", "cred-*"))
		Expect(globErr).ToNot(HaveOccurred())
		Expect(credentialDirs).To(BeEmpty())
	})

	It("makes cleanup failures observable without exposing sensitive values", func() {
		const sensitiveValue = "cleanup-sensitive-path"
		details := &ExecDetails{ExitCode: 0}
		returned, err := finishRun(details, nil, errors.New("remove "+sensitiveValue), []string{sensitiveValue})

		Expect(returned).To(BeIdenticalTo(details))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("[REDACTED]"))
		Expect(err.Error()).ToNot(ContainSubstring(sensitiveValue))
		Expect(details.Error).To(MatchError(err))
	})

	It("accepts configured success exit codes while defaulting to zero", func() {
		base := Exec{Chroot: GinkgoT().TempDir(), BaseDir: GinkgoT().TempDir()}
		failed, err := RunCmd(shellContext(stdcontext.Background()), base, exec.Command("sh", "-c", "exit 3"))
		Expect(err).To(HaveOccurred())
		Expect(failed.ExitCode).To(Equal(3))

		base.SuccessExitCodes = []int{3}
		succeeded, err := RunCmd(shellContext(stdcontext.Background()), base, exec.Command("sh", "-c", "exit 3"))
		Expect(err).ToNot(HaveOccurred())
		Expect(succeeded.ExitCode).To(Equal(3))
		Expect(succeeded.Error).ToNot(HaveOccurred())
	})

	It("uses safe display arguments in results and errors", func() {
		const sensitiveArg = "argument-sensitive-value"
		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:      GinkgoT().TempDir(),
			BaseDir:     GinkgoT().TempDir(),
			DisplayPath: "prowler",
			DisplayArgs: []string{"gcp", "--credentials-file", "[REDACTED]"},
		}, exec.Command("sh", "-c", "exit 4", sensitiveArg))

		Expect(err).To(HaveOccurred())
		Expect(details.Path).To(Equal("prowler"))
		Expect(details.Args).To(Equal([]string{"gcp", "--credentials-file", "[REDACTED]"}))
		Expect(err.Error()).ToNot(ContainSubstring(sensitiveArg))
	})

	It("kills the process group when its context is cancelled", func() {
		base, cancel := stdcontext.WithCancel(stdcontext.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		started := time.Now()

		details, err := RunCmd(shellContext(base), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
		}, exec.Command("sh", "-c", `trap '' TERM; sleep 60 & wait`))

		Expect(errors.Is(err, stdcontext.Canceled)).To(BeTrue())
		Expect(details).ToNot(BeNil())
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
	})

	It("preserves command stdin", func() {
		command := exec.Command("sh", "-c", "read value; printf %s \"$value\"")
		command.Stdin = strings.NewReader("stdin-value\n")
		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("stdin-value"))
	})
})

func shellContext(ctx stdcontext.Context) dbcontext.Context {
	return dbcontext.NewContext(ctx)
}
