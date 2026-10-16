package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
)

// CaptureMode is how a checkpoint dumps a process's stacks.
type CaptureMode string

const (
	// CaptureInProcess dumps the goroutines of the host, the one process
	// whose stacks a capture can read from inside it.
	CaptureInProcess CaptureMode = "inprocess"
	// CaptureNative samples any process with the OS's stack sampler, without
	// stopping it: sample on darwin, eu-stack or gdb on linux.
	CaptureNative CaptureMode = "native"
)

// nativeTool is an OS stack sampler and the arguments it samples pid with.
type nativeTool struct {
	name string
	args func(pid string) []string
}

// nativeTools are the samplers of each OS, in the order they are preferred.
var nativeTools = map[string][]nativeTool{
	"darwin": {{name: "sample", args: func(pid string) []string { return []string{pid, "1", "-file", "/dev/stdout"} }}},
	"linux": {
		{name: "eu-stack", args: func(pid string) []string { return []string{"-p", pid} }},
		{name: "gdb", args: func(pid string) []string { return []string{"-batch", "-ex", "thread apply all bt", "-p", pid} }},
	},
}

// nativeSampler is the OS stack sampler found on the host's PATH, or why
// none is.
type nativeSampler struct {
	path string
	tool nativeTool
	run  func(ctx context.Context, path string, args ...string) (string, error)
	err  error
}

func findNativeSampler(lookPath func(string) (string, error), run func(context.Context, string, ...string) (string, error)) nativeSampler {
	tools, ok := nativeTools[runtime.GOOS]
	if !ok {
		return nativeSampler{err: fmt.Errorf("no native stack sampler for %s", runtime.GOOS)}
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if path, err := lookPath(tool.name); err == nil {
			return nativeSampler{path: path, tool: tool, run: run}
		}
		names = append(names, tool.name)
	}
	return nativeSampler{err: fmt.Errorf("native stacks need %s on PATH", strings.Join(names, " or "))}
}

func (s nativeSampler) dump(ctx context.Context, pid int) (string, error) {
	return s.run(ctx, s.path, s.tool.args(strconv.Itoa(pid))...)
}

// runCommand runs a native sampler, its output the stacks it dumped.
func runCommand(ctx context.Context, path string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// hostStack dumps the goroutines of the host.
func hostStack() (string, error) {
	var dump bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		return "", fmt.Errorf("dump goroutines: %w", err)
	}
	return dump.String(), nil
}

// captureModes are the modes the stacks of pid can be dumped with.
func (t tree) captureModes(pid int) []string {
	var modes []string
	for _, mode := range []CaptureMode{CaptureInProcess, CaptureNative} {
		if t.available(mode, pid) == nil {
			modes = append(modes, string(mode))
		}
	}
	return modes
}

// available is why the stacks of pid cannot be dumped with mode, or nil.
func (t tree) available(mode CaptureMode, pid int) error {
	if mode == CaptureNative {
		return t.native.err
	}
	if pid != os.Getpid() {
		return fmt.Errorf("process %d is not the host process %d, whose stacks alone are dumped %s", pid, os.Getpid(), CaptureInProcess)
	}
	return nil
}

// dump dumps the stacks of pid with mode, which is available for it.
func (t tree) dump(ctx context.Context, mode CaptureMode, pid int) (string, error) {
	if mode == CaptureNative {
		return t.native.dump(ctx, pid)
	}
	return hostStack()
}
