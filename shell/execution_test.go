package shell

import (
	"bytes"
	stdcontext "context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/collections"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	"github.com/samber/lo"
)

func TestShell(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Shell Suite")
}

func TestEnv(t *testing.T) {
	testData := []struct {
		name         string
		exec         Exec
		expectedVars []string
	}{
		{
			name: "access custom env vars",
			exec: Exec{
				Script: "env",
				EnvVars: []types.EnvVar{
					{Name: "mc_test_secret", ValueStatic: "abcdef"},
				},
			},
			expectedVars: []string{"mc_test_secret"},
		},
		{
			name: "access multiple custom env vars",
			exec: Exec{
				Script: "env",
				EnvVars: []types.EnvVar{
					{Name: "mc_test_secret_key", ValueStatic: "abc"},
					{Name: "mc_test_secret_id", ValueStatic: "xyz"},
				},
			},
			expectedVars: []string{"mc_test_secret_key", "mc_test_secret_id"},
		},
		{
			name: "no access to process env",
			exec: Exec{
				Script: "env",
			},
			expectedVars: []string{},
		},
	}

	ctx := dbcontext.New()
	for _, td := range testData {
		t.Run(td.name, func(t *testing.T) {
			result, err := Run(ctx, td.exec)
			if err != nil {
				t.Fatalf("failed to run command %s", err)
			}

			if result.ExitCode != 0 {
				t.Errorf("unexpected non-zero exit code: %d", result.ExitCode)
			}

			if result.Stderr != "" {
				t.Errorf("unexpected stderr: %s", result.Stderr)
			}

			envVars := strings.Split(result.Stdout, "\n")

			// These env vars are always made available.
			envVars = lo.Filter(envVars, func(v string, _ int) bool {
				key, _, _ := strings.Cut(v, "=")
				return key != "PWD" && key != "SHLVL" && key != "_"
			})

			envVarKeys := lo.Map(envVars, func(v string, _ int) string {
				key, _, _ := strings.Cut(v, "=")
				return key
			})

			expected := collections.MapKeys(allowedEnvVars)
			expected = append(expected, td.expectedVars...)
			if !lo.Every(expected, envVarKeys) {
				t.Errorf("expected %s, got %s", td.expectedVars, envVarKeys)
			}
		})

		os.RemoveAll("./shell-tmp/")
	}
}

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

	DescribeTable("detects the interpreter from the shebang",
		func(script, interpreter string, args []string) {
			gotInterpreter, gotArgs := DetectInterpreterFromShebang(script)
			Expect(gotInterpreter).To(Equal(interpreter))
			Expect(gotArgs).To(Equal(args))
		},
		Entry("a bare interpreter takes the script with -c", "#!/bin/sh\necho", "/bin/sh", []string{"-c"}),
		Entry("env resolving an interpreter takes the script with -c", "#!/usr/bin/env python3\nprint()", "/usr/bin/env", []string{"python3", "-c"}),
		Entry("explicit interpreter arguments are kept as written", "#!/bin/bash -euc\necho", "/bin/bash", []string{"-euc"}),
		Entry("env with explicit arguments is kept as written", "#!/usr/bin/env python3 -c\nprint()", "/usr/bin/env", []string{"python3", "-c"}),
		// node's -c is --check, which reads a file: these take inline code with -e.
		Entry("node takes the script with -e", "#!/usr/local/bin/node\nconsole.log()", "/usr/local/bin/node", []string{"-e"}),
		Entry("env resolving node takes the script with -e", "#!/usr/bin/env node\nconsole.log()", "/usr/bin/env", []string{"node", "-e"}),
		Entry("ruby takes the script with -e", "#!/usr/bin/env ruby\nputs", "/usr/bin/env", []string{"ruby", "-e"}),
		Entry("perl takes the script with -e", "#!/usr/bin/perl\nprint", "/usr/bin/perl", []string{"-e"}),
	)

	It("runs a node script that reads its input from stdin", func() {
		if _, err := exec.LookPath("node"); err != nil {
			Skip("node is not installed")
		}
		command, err := CreateCommandFromScript(shellContext(stdcontext.Background()),
			"#!/usr/bin/env node\nconst input = JSON.parse(require(\"node:fs\").readFileSync(0, \"utf8\"));\nprocess.stdout.write(input.greeting);")
		Expect(err).ToNot(HaveOccurred())
		command.Stdin = strings.NewReader(`{"greeting":"hello from node"}`)

		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("hello from node"))
	})

	It("runs a script whose shebang resolves its interpreter through env", func() {
		command, err := CreateCommandFromScript(shellContext(stdcontext.Background()), "#!/usr/bin/env sh\necho \"$0\"")
		Expect(err).ToNot(HaveOccurred())

		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
		}, command)

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("sh"))
	})

	It("runs many commands against one prepared environment, redacting each", func() {
		const sensitiveValue = "prepared-sensitive-value"
		prepared, err := SetupEnv(shellContext(stdcontext.Background()), &Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			EnvVars: []types.EnvVar{{Name: "SHELL_TOKEN", ValueStatic: sensitiveValue}},
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(prepared.Cleanup)

		for range 2 {
			details, err := prepared.Run(shellContext(stdcontext.Background()), exec.Command("sh", "-c", `printf %s "$SHELL_TOKEN"`), RunOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(details.Stdout).To(Equal("[REDACTED]"))
		}
	})

	It("does not let a wrapped run change the prepared environment", func() {
		prepared, err := SetupEnv(shellContext(stdcontext.Background()), &Exec{Chroot: GinkgoT().TempDir(), BaseDir: GinkgoT().TempDir()})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(prepared.Cleanup)
		before := slices.Clone(prepared.Env)

		_, err = prepared.Run(shellContext(stdcontext.Background()), exec.Command("sh", "-c", "true"), RunOptions{
			Wrap: func(_ stdcontext.Context, cmd string, args, _ []string) (string, []string, []string, error) {
				return cmd, args, []string{"PATH=/usr/bin:/bin", "WRAPPED=1"}, nil
			},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(prepared.Env).To(Equal(before))
	})

	It("writes connection credential files into CredentialsDir, not the command directory", func() {
		workDir, credsDir := GinkgoT().TempDir(), GinkgoT().TempDir()
		prepared, err := SetupEnv(shellContext(stdcontext.Background()), &Exec{
			Chroot:         workDir,
			BaseDir:        GinkgoT().TempDir(),
			CredentialsDir: credsDir,
			Connections: connection.ExecConnections{AWS: &connection.AWSConnection{
				AccessKey: types.EnvVar{ValueStatic: "creds-dir-access-key"},
				SecretKey: types.EnvVar{ValueStatic: "creds-dir-secret-key"},
			}},
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(prepared.Cleanup)

		Expect(filepath.Join(workDir, ".creds")).ToNot(BeAnExistingFile())
		inCredsDir, err := filepath.Glob(filepath.Join(credsDir, ".creds", "*"))
		Expect(err).ToNot(HaveOccurred())
		Expect(inCredsDir).ToNot(BeEmpty())
		Expect(prepared.Cwd).To(Equal(workDir))
	})

	It("points credential env vars at absolute paths when CredentialsDir is relative", func() {
		credsDir := GinkgoT().TempDir()
		testDir, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		relativeCredsDir, err := filepath.Rel(testDir, credsDir)
		Expect(err).ToNot(HaveOccurred())
		prepared, err := SetupEnv(shellContext(stdcontext.Background()), &Exec{
			Chroot:         GinkgoT().TempDir(),
			BaseDir:        GinkgoT().TempDir(),
			CredentialsDir: relativeCredsDir,
			Connections: connection.ExecConnections{AWS: &connection.AWSConnection{
				AccessKey: types.EnvVar{ValueStatic: "relative-creds-access-key"},
				SecretKey: types.EnvVar{ValueStatic: "relative-creds-secret-key"},
			}},
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(prepared.Cleanup)

		credentialsFile, found := lo.Find(prepared.Env, func(env string) bool { return strings.HasPrefix(env, "AWS_SHARED_CREDENTIALS_FILE=") })
		Expect(found).To(BeTrue())
		credentialsPath := strings.TrimPrefix(credentialsFile, "AWS_SHARED_CREDENTIALS_FILE=")
		Expect(filepath.IsAbs(credentialsPath)).To(BeTrue(), credentialsPath)
		Expect(credentialsPath).To(BeAnExistingFile())
	})

	It("runs the command through Wrap with the prepared environment", func() {
		var seenEnv []string
		details, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			EnvVars: []types.EnvVar{{Name: "SHELL_DECLARED", ValueStatic: "declared"}},
			Wrap: func(_ stdcontext.Context, cmd string, args, env []string) (string, []string, []string, error) {
				seenEnv = env
				return "env", append([]string{"SHELL_WRAPPED=wrapped", cmd}, args...), nil, nil
			},
		}, exec.Command("sh", "-c", `printf '%s %s' "$SHELL_WRAPPED" "$SHELL_DECLARED"`))

		Expect(err).ToNot(HaveOccurred())
		Expect(details.Stdout).To(Equal("wrapped [REDACTED]"))
		Expect(seenEnv).To(ContainElement("SHELL_DECLARED=declared"))
	})

	It("does not start the command when Wrap fails", func() {
		marker := filepath.Join(GinkgoT().TempDir(), "started")
		_, err := RunCmd(shellContext(stdcontext.Background()), Exec{
			Chroot:  GinkgoT().TempDir(),
			BaseDir: GinkgoT().TempDir(),
			Wrap: func(stdcontext.Context, string, []string, []string) (string, []string, []string, error) {
				return "", nil, nil, errors.New("sandbox unavailable")
			},
		}, exec.Command("sh", "-c", "touch "+marker))

		Expect(err).To(MatchError(ContainSubstring("sandbox unavailable")))
		Expect(marker).ToNot(BeAnExistingFile())
	})
})

func shellContext(ctx stdcontext.Context) dbcontext.Context {
	return dbcontext.NewContext(ctx)
}
