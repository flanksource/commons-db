package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
)

type objectStream struct {
	ctx    context.Context
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
	finish func(error)
	closed bool
}

func openObjectStream(ctx context.Context, options command) (stream *objectStream, err error) {
	finish := startGitTiming(ctx, "commands")
	defer func() {
		if err != nil {
			finish(err)
		}
	}()
	if err := requireGit(ctx); err != nil {
		return nil, err
	}
	stream = &objectStream{ctx: ctx, finish: finish, cmd: exec.CommandContext(ctx, "git", "cat-file", "--batch", "-Z")}
	stream.cmd.Dir, stream.cmd.Env = options.dir, options.env
	if stream.cmd.Env == nil {
		stream.cmd.Env = baseEnv()
	}
	stream.cmd.Stderr = &stream.stderr
	stream.input, err = stream.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := stream.cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(err, stream.input.Close())
	}
	stream.output = bufio.NewReader(output)
	if err := stream.cmd.Start(); err != nil {
		return nil, errors.Join(err, stream.input.Close(), output.Close())
	}
	return stream, nil
}

func (stream *objectStream) read(object string, read func(batchBlob, io.Reader) error) error {
	if stream.closed {
		return fs.ErrClosed
	}
	if err := stream.ctx.Err(); err != nil {
		return stream.stop(err)
	}
	if _, err := io.WriteString(stream.input, object+"\x00"); err != nil {
		return stream.stop(err)
	}
	if err := readBatchObject(stream.output, object, read); err != nil {
		return stream.stop(err)
	}
	return nil
}

func (stream *objectStream) close() error { return stream.stop(nil) }

func (stream *objectStream) stop(cause error) error {
	if stream.closed {
		return nil
	}
	stream.closed = true
	if cause != nil {
		if err := stream.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cause = errors.Join(cause, err)
		}
	}
	err := errors.Join(cause, stream.input.Close())
	if waitErr := stream.cmd.Wait(); waitErr != nil {
		exit, terminated := errors.AsType[*exec.ExitError](waitErr)
		requestErr := stream.ctx.Err()
		cancelledWorker := cause == nil && requestErr != nil && (errors.Is(waitErr, requestErr) || terminated && exit.ExitCode() == -1)
		if !cancelledWorker {
			err = errors.Join(err, fmt.Errorf("git cat-file --batch: %w: %s", waitErr, Redact(stream.stderr.String())))
		}
	}
	if cause != nil {
		err = errors.Join(err, stream.ctx.Err())
	}
	stream.finish(err)
	return err
}
