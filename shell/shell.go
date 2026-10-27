package shell

import (
	gocontext "context"
	"errors"
	"fmt"
	"io"
	"os"
	osExec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	clickyexec "github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	fileUtils "github.com/flanksource/commons/files"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"github.com/samber/oops"
)

// List of env var keys that we pass on to the exec command
var allowedEnvVars = map[string]struct{}{
	"CLOUDSDK_PYTHON":                       {},
	"DEBIAN_FRONTEND":                       {},
	"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": {},
	"HOME":                                  {},
	"LC_CTYPE":                              {},
	"PATH":                                  {},
	"PS_INSTALL_FOLDER":                     {},
	"PS_VERSION":                            {},
	"PSModuleAnalysisCachePath":             {},
	"USER":                                  {},
	"MANPATH":                               {},
	"TERM":                                  {},
	"LANG":                                  {},
	"SHELL":                                 {},
	"SHLVL":                                 {},
	"LC_ALL":                                {},
	"JAVA_HOME":                             {},
	"SDKMAN_DIR":                            {},
	"LSCOLORS":                              {},
	"CLICOLOR":                              {},
	"COLORTERM":                             {},
	"TERM_PROGRAM":                          {},
	"TERM_PROGRAM_VERSION":                  {},
	"COLORFGBG":                             {},
}

func init() {
	for _, env := range strings.Split(properties.String("", "shell.allowed.envs"), ",") {
		logger.V(5).Infof("allowing env var %s", env)
		allowedEnvVars[env] = struct{}{}
	}
}

type Exec struct {
	Script      string
	Connections connection.ExecConnections
	Checkout    *connection.GitConnection
	Artifacts   []Artifact

	EnvVars []types.EnvVar
	DotEnv  []string
	Chroot  string
	BaseDir string
	// CredentialsDir is where connection credential files are written; empty
	// writes them into the command's working directory.
	CredentialsDir string

	PassthroughEnv   []string
	SuccessExitCodes []int
	DisplayPath      string
	DisplayArgs      []string

	// Wrap rewrites the command for confined execution (e.g. a sandbox) after the
	// environment is prepared. A nil env keeps the prepared environment.
	Wrap WrapFunc
}

// WrapFunc rewrites a command, its arguments and environment before it starts.
type WrapFunc func(ctx gocontext.Context, cmd string, args, env []string) (string, []string, []string, error)

// +kubebuilder:object:generate=true
type Artifact struct {
	Path    string        `json:"path" yaml:"path" template:"true"`
	Content io.ReadCloser `json:"-" yaml:"-"`
	// Content is the content of the artifact. If Path is /dev/stdout or /dev/stderr, Content will be populated with the respective output.
}

type ExecDetails struct {
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exitCode"`
	Path     string   `json:"path"`
	Args     []string `json:"args"`

	// Any extra details about the command execution, e.g. git commit id, etc.
	Extra map[string]any `json:"extra,omitempty"`

	Error     error      `json:"-" yaml:"-"`
	Artifacts []Artifact `json:"-" yaml:"-"`
}

func (e ExecDetails) String() string {
	return fmt.Sprintf("%s %s exit=%d stdout=%s stderr=%s", e.Path, e.Args, e.ExitCode, e.Stdout, e.Stderr)
}

func (e *ExecDetails) GetArtifacts() []Artifact {
	if e == nil {
		return nil
	}
	return e.Artifacts
}

func JQ(ctx context.Context, path string, script string) (string, error) {
	_ctx, cancel := gocontext.WithTimeout(ctx, properties.Duration(5*time.Second, "shell.jq.timeout"))
	defer cancel()
	dir, file := splitCommandPath(path)
	cmd := osExec.CommandContext(_ctx, "jq", script, file)
	result, err := RunCmd(ctx.Wrap(_ctx), Exec{
		Chroot: dir,
	}, cmd)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func YQ(ctx context.Context, path string, script string) (string, error) {
	_ctx, cancel := gocontext.WithTimeout(ctx, properties.Duration(5*time.Second, "shell.yq.timeout", "shell.jq.timeout"))
	defer cancel()
	dir, file := splitCommandPath(path)
	cmd := osExec.CommandContext(_ctx, "yq", script, file)
	result, err := RunCmd(ctx.Wrap(_ctx), Exec{
		Chroot: dir,
	}, cmd)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func Run(ctx context.Context, exec Exec) (*ExecDetails, error) {
	cmd, err := CreateCommandFromScript(ctx, exec.Script)
	if err != nil {
		return nil, oops.Hint(exec.Script).Wrap(err)
	}

	return RunCmd(ctx, exec, cmd)
}

func RunCmd(ctx context.Context, exec Exec, cmd *osExec.Cmd) (*ExecDetails, error) {
	if cmd == nil {
		return nil, ctx.Oops().Errorf("shell command is required")
	}
	setup, err := SetupEnv(ctx, &exec)
	if err != nil {
		return nil, ctx.Oops().Wrap(err)
	}
	result, runErr := setup.Run(ctx, cmd, RunOptions{
		Wrap:             exec.Wrap,
		SuccessExitCodes: exec.SuccessExitCodes,
		DisplayPath:      exec.DisplayPath,
		DisplayArgs:      exec.DisplayArgs,
	})
	return finishRun(result, runErr, setup.Cleanup(), setup.sensitiveValues)
}

// RunOptions are the per-command settings of a run against a prepared setup.
type RunOptions struct {
	// Wrap rewrites the command for confined execution; a nil env from it keeps
	// the prepared environment.
	Wrap             WrapFunc
	SuccessExitCodes []int
	DisplayPath      string
	DisplayArgs      []string
}

// Run executes cmd in the prepared environment without preparing it again, so
// one setup — its connections, credential files and resolved secrets — serves
// many commands. Output is redacted against the setup's sensitive values on
// every run. The setup is never modified, so concurrent runs are safe; the
// caller still owns Cleanup.
func (s *SetupResult) Run(ctx context.Context, cmd *osExec.Cmd, opts RunOptions) (*ExecDetails, error) {
	if cmd == nil {
		return nil, ctx.Oops().Errorf("shell command is required")
	}
	env, err := wrapCommand(ctx, opts.Wrap, cmd, s.Env, s.sensitiveValues)
	if err != nil {
		return nil, err
	}
	return runCmd(ctx, &commandContext{
		cmd:              cmd,
		artifacts:        s.Artifacts,
		extra:            s.Extra,
		mountPoint:       s.Cwd,
		env:              env,
		sensitiveValues:  s.sensitiveValues,
		successExitCodes: opts.SuccessExitCodes,
		displayPath:      opts.DisplayPath,
		displayArgs:      opts.DisplayArgs,
	})
}

func wrapCommand(ctx context.Context, wrap WrapFunc, cmd *osExec.Cmd, env, sensitiveValues []string) ([]string, error) {
	if wrap == nil {
		return env, nil
	}
	path, args, wrappedEnv, err := wrap(ctx, cmd.Path, cmd.Args[1:], slices.Clone(env))
	if err != nil {
		return nil, fmt.Errorf("wrapping %s for confined execution: %s", cmd.Path, redactText(err.Error(), sensitiveValues))
	}
	cmd.Path, cmd.Args = path, append([]string{path}, args...)
	if wrappedEnv == nil {
		return env, nil
	}
	return wrappedEnv, nil
}

func finishRun(result *ExecDetails, runErr, cleanupErr error, sensitiveValues []string) (*ExecDetails, error) {
	if cleanupErr == nil {
		return result, runErr
	}
	safeCleanupErr := fmt.Errorf("cleanup shell setup: %s", redactText(cleanupErr.Error(), sensitiveValues))
	if runErr != nil {
		safeCleanupErr = errors.Join(runErr, safeCleanupErr)
	}
	if result != nil {
		result.Error = safeCleanupErr
	}
	return result, safeCleanupErr
}

func splitCommandPath(path string) (dir, file string) {
	if path == "" {
		return ".", path
	}
	dir = filepath.Dir(path)
	file = filepath.Base(path)
	if dir == "" {
		dir = "."
	}
	return dir, file
}

type commandContext struct {
	cmd              *osExec.Cmd
	artifacts        []Artifact
	extra            map[string]any
	env              []string
	sensitiveValues  []string
	successExitCodes []int
	displayPath      string
	displayArgs      []string
	envs             []string

	// Working directory for the command
	mountPoint string
}

func runCmd(ctx context.Context, command *commandContext) (*ExecDetails, error) {
	displayPath, displayArgs := command.display()
	ctx.Logger.V(3).Infof("running: %s %v", displayPath, displayArgs)

	stdoutLive := redactingWriterFor(command.cmd.Stdout, command.sensitiveValues)
	stderrLive := redactingWriterFor(command.cmd.Stderr, command.sensitiveValues)
	process := clickyexec.NewExec(command.cmd.Path, command.cmd.Args[1:]...).
		WithoutShell().
		WithCwd(command.mountPoint).
		WithExactEnv(environmentMap(command.env)).
		WithProcessGroup().
		WithStdin(command.cmd.Stdin).
		WithLogger(logger.NewWithWriter(io.Discard)).
		Stream(stdoutLive, stderrLive)

	processResult, executionErr := runProcess(ctx, process)
	closeErr := closeRedactors(stdoutLive, stderrLive)
	result := ExecDetails{
		Stdout:   strings.TrimSpace(redactText(processResult.Stdout, command.sensitiveValues)),
		Stderr:   strings.TrimSpace(redactText(processResult.Stderr, command.sensitiveValues)),
		ExitCode: processResult.ExitCode,
		Path:     displayPath,
		Args:     displayArgs,
		Extra:    command.extra,
	}
	ctx.Logger.V(3).Infof("%s exited with code=%d, stdout=%d bytes, stderr=%d bytes", displayPath, result.ExitCode, len(result.Stdout), len(result.Stderr))

	for _, artifactConfig := range command.artifacts {
		switch artifactConfig.Path {
		case "/dev/stdout":
			result.Artifacts = append(result.Artifacts, Artifact{
				Content: io.NopCloser(strings.NewReader(result.Stdout)),
				Path:    "stdout",
			})

		case "/dev/stderr":
			result.Artifacts = append(result.Artifacts, Artifact{
				Content: io.NopCloser(strings.NewReader(result.Stderr)),
				Path:    "stderr",
			})

		default:
			paths, err := fileUtils.DoubleStarGlob(command.mountPoint, []string{artifactConfig.Path})
			if err != nil {
				return nil, fmt.Errorf("resolve artifact files: %s", redactText(err.Error(), command.sensitiveValues))
			}

			for _, path := range paths {
				file, err := os.Open(path)
				if err != nil {
					return nil, artifactFileError("open", path, err, command.sensitiveValues)
				}

				if stat, err := file.Stat(); err != nil {
					_ = file.Close()
					return nil, artifactFileError("stat", path, err, command.sensitiveValues)
				} else if stat.IsDir() {
					_ = file.Close()
					return nil, fmt.Errorf("artifact path (%s) is a directory; expected file", redactText(path, command.sensitiveValues))
				}

				result.Artifacts = append(result.Artifacts, Artifact{
					Content: newRedactingReadCloser(file, command.sensitiveValues),
					Path:    redactText(path, command.sensitiveValues),
				})
			}
		}
	}
	if executionErr != nil {
		result.Error = executionErr
		return &result, executionErr
	}
	if closeErr != nil {
		result.Error = closeErr
		return &result, closeErr
	}
	if !successCode(result.ExitCode, command.successExitCodes) {
		result.Error = fmt.Errorf("command exited with %d", result.ExitCode)
		return &result, ctx.Oops().With(
			"cmd", displayPath,
			"args", displayArgs,
			"extra", result.Extra,
			"exit-code", result.ExitCode,
		).Code(fmt.Sprintf("exited with %d", result.ExitCode)).Wrap(result.Error)
	}
	return &result, nil
}

func artifactFileError(action, path string, err error, sensitiveValues []string) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("%s artifact file path=%s: %w", action, redactText(path, sensitiveValues), err)
}
