package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	gitcommand "github.com/flanksource/commons-db/git/internal/command"
)

// visibilityEnvironment imports only the user's excludesFile; other global settings stay isolated.
func visibilityEnvironment(ctx context.Context, directory string) ([]string, error) {
	local, err := gitcommand.Run(ctx, gitcommand.Options{Directory: directory, Args: []string{"config", "--path", "--get", "core.excludesFile"}})
	if err == nil {
		return baseEnv(), nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return nil, fmt.Errorf("read repository excludesFile: %w: %s", err, local.Stderr)
	}
	env := []string{}
	for _, entry := range baseEnv() {
		if !strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") {
			env = append(env, entry)
		}
	}
	if global, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		env = append(env, "GIT_CONFIG_GLOBAL="+global)
	}
	global, err := gitcommand.Run(ctx, gitcommand.Options{Directory: directory, Environment: env, Args: []string{"config", "--global", "--includes", "--path", "--get", "core.excludesFile"}})
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return baseEnv(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read global excludesFile: %w: %s", err, global.Stderr)
	}
	return baseEnv(configPair{key: "core.excludesFile", value: strings.TrimSuffix(string(global.Output), "\n")}), nil
}
