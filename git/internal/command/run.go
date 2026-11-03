package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

type Options struct {
	Directory   string
	Args        []string
	Environment []string
	Input       []byte
	Stderr      io.Writer
}

type Result struct {
	Output []byte
	Stderr string
	State  *os.ProcessState
}

func Run(ctx context.Context, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	finish := StartTiming(ctx, "commands")
	cmd := exec.CommandContext(ctx, "git", options.Args...)
	cmd.Dir, cmd.Env = options.Directory, options.Environment
	if cmd.Env == nil {
		cmd.Env = Environment()
	}
	if options.Input != nil {
		cmd.Stdin = bytes.NewReader(options.Input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if options.Stderr != nil {
		cmd.Stderr = options.Stderr
	}
	err := cmd.Run()
	err = errors.Join(err, ctx.Err())
	finish(err, subcommand(options.Args), cmd.ProcessState)
	return Result{Output: stdout.Bytes(), Stderr: stderr.String(), State: cmd.ProcessState}, err
}
