package shell

import (
	"bytes"
	gocontext "context"
	"fmt"
	"io"
	"os"
	osExec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	fileUtils "github.com/flanksource/commons/files"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	"github.com/samber/lo"
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
}

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
	result, err := RunCmd(ctx, Exec{
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
	result, err := RunCmd(ctx, Exec{
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
	ctx.Logger.V(3).Infof("running: %s %s", cmd.Path, lo.Map(cmd.Args, func(arg string, _ int) string { return strings.TrimSpace(arg) }))
	setup, err := SetupEnv(ctx, &exec)
	if err != nil {
		return nil, ctx.Oops().Wrap(err)
	}
	defer func() {
		if err := setup.Cleanup(); err != nil {
			logger.Errorf("failed to cleanup shell setup artifacts: %v", err)
		}
	}()

	cmd.Dir = setup.Cwd
	cmd.Env = setup.Env

	return runCmd(ctx, &commandContext{
		cmd:        cmd,
		artifacts:  setup.Artifacts,
		extra:      setup.Extra,
		mountPoint: setup.Cwd,
	})
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
	cmd       *osExec.Cmd
	artifacts []Artifact
	EnvVars   []types.EnvVar
	extra     map[string]any

	// Working directory for the command
	mountPoint string

	// Additional env vars to be exported into the shell
	envs []string
}

func runCmd(ctx context.Context, cmd *commandContext) (*ExecDetails, error) {
	var (
		result ExecDetails
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	cmd.cmd.Stdout = &stdout
	cmd.cmd.Stderr = &stderr

	ctx.Logger.V(6).Infof("working directory: %s\nenvironment:\n%s", cmd.mountPoint, strings.Join(cmd.cmd.Env, "\n"))

	result.Error = cmd.cmd.Run()
	result.Args = cmd.cmd.Args
	result.Extra = cmd.extra
	result.Path = cmd.cmd.Path
	result.ExitCode = cmd.cmd.ProcessState.ExitCode()
	result.Stderr = strings.TrimSpace(stderr.String())
	result.Stdout = strings.TrimSpace(stdout.String())

	ctx.Logger.V(3).Infof("%s exited with code=%d, stdout=%d bytes, stderr=%d bytes", cmd.cmd.Path, result.ExitCode, len(result.Stdout), len(result.Stderr))

	for _, artifactConfig := range cmd.artifacts {
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
			paths, err := fileUtils.DoubleStarGlob(cmd.mountPoint, []string{artifactConfig.Path})
			if err != nil {
				return nil, err
			}

			for _, path := range paths {
				file, err := os.Open(path)
				if err != nil {
					return nil, fmt.Errorf("error opening artifact file. path=%s; %w", path, err)
				}

				if stat, err := file.Stat(); err != nil {
					return nil, fmt.Errorf("error getting artifact file stat. path=%s; %w", path, err)
				} else if stat.IsDir() {
					return nil, fmt.Errorf("artifact path (%s) is a directory. expected file", path)
				}

				result.Artifacts = append(result.Artifacts, Artifact{
					Content: file,
					Path:    path,
				})
			}
		}
	}
	if result.ExitCode != 0 {
		return &result, ctx.Oops().With(
			"cmd", cmd.cmd.Path,
			"args", cmd.cmd.Args,
			"error", result.Error.Error(),
			"stderr", result.Stderr,
			"stdout", result.Stdout,
			"extra", result.Extra,
			"exit-code", result.ExitCode,
		).Code(fmt.Sprintf("exited with %d", result.ExitCode)).Errorf("%v", result.Error.Error())
	}

	return &result, nil
}
