// Package gittest builds isolated local Git fixtures through the shared runner.
package gittest

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/git/internal/command"
)

type Options struct {
	Directory   string
	Args        []string
	Environment []string
	Input       []byte
}

func Init(ctx context.Context, directory string) error {
	_, err := Run(ctx, Options{Directory: directory, Args: []string{"init", "--quiet", "--initial-branch=main"}})
	return err
}

func Run(ctx context.Context, options Options) ([]byte, error) {
	env := command.Environment(command.Config{Key: "user.name", Value: "Fixture"}, command.Config{Key: "user.email", Value: "fixture@example.test"})
	for _, entry := range options.Environment {
		if strings.HasPrefix(entry, "GIT_AUTHOR_") || strings.HasPrefix(entry, "GIT_COMMITTER_") {
			env = append(env, entry)
		}
	}
	result, err := command.Run(ctx, command.Options{Directory: options.Directory, Args: options.Args, Input: options.Input, Environment: env})
	if err != nil {
		return result.Output, fmt.Errorf("git fixture %s: %w: %s", strings.Join(options.Args, " "), err, result.Stderr)
	}
	return result.Output, nil
}
