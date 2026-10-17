package stacks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/google/gops/signal"
)

// The transports commons-db ships.
const (
	TransportInProcess = "inprocess"
	TransportNative    = "native"
	TransportPProf     = "pprof"
	TransportGops      = "gops"
)

type inProcess struct{}

// InProcess dumps the goroutines of the host, the one process whose stacks
// it can read from inside it.
func InProcess() Dumper { return inProcess{} }

func (inProcess) Transport() string { return TransportInProcess }
func (inProcess) Destructive() bool { return false }

func (inProcess) Available(pid int) error {
	if pid != os.Getpid() {
		return fmt.Errorf("process %d is not the host process %d", pid, os.Getpid())
	}
	return nil
}

func (inProcess) Dump(_ context.Context, _ int) (Dump, error) {
	var dump bytes.Buffer
	collectedAt := time.Now().UTC()
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		return Dump{}, fmt.Errorf("dump goroutines: %w", err)
	}
	return Dump{Format: FormatGo, Text: dump.String(), CollectedAt: collectedAt}, nil
}

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

// NativeOptions are how the native dumper finds and runs the OS's sampler.
type NativeOptions struct {
	// LookPath finds the sampler; nil is exec.LookPath.
	LookPath func(file string) (string, error)
	// Run runs the sampler at path, its output the stacks it dumped; nil runs
	// it as a child process.
	Run func(ctx context.Context, path string, args ...string) (string, error)
}

type native struct {
	path string
	tool nativeTool
	run  func(ctx context.Context, path string, args ...string) (string, error)
	err  error
}

// Native samples any process's threads with the OS's stack sampler, without
// stopping it: sample on darwin, eu-stack or gdb on linux, looked up once,
// here.
func Native(options NativeOptions) Dumper {
	lookPath, run := options.LookPath, options.Run
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if run == nil {
		run = runCommand
	}
	tools, ok := nativeTools[runtime.GOOS]
	if !ok {
		return native{err: fmt.Errorf("no native stack sampler for %s", runtime.GOOS)}
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if path, err := lookPath(tool.name); err == nil {
			return native{path: path, tool: tool, run: run}
		}
		names = append(names, tool.name)
	}
	return native{err: fmt.Errorf("native stacks need %s on PATH", strings.Join(names, " or "))}
}

func (native) Transport() string       { return TransportNative }
func (native) Destructive() bool       { return false }
func (n native) Available(_ int) error { return n.err }
func (n native) Dump(ctx context.Context, pid int) (Dump, error) {
	if n.err != nil {
		return Dump{}, n.err
	}
	collectedAt := time.Now().UTC()
	text, err := n.run(ctx, n.path, n.tool.args(strconv.Itoa(pid))...)
	if err != nil {
		return Dump{}, err
	}
	return Dump{Format: FormatNative, Text: text, CollectedAt: collectedAt}, nil
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

// addressed is a dumper that reads a Go process's goroutines over the
// address a host gives for it.
type addressed struct {
	transport string
	address   AddressFunc
	dump      func(ctx context.Context, addr string) (string, error)
}

// PProf dumps the goroutines of a Go process that serves net/http/pprof at
// the address address gives (/debug/pprof/goroutine?debug=2).
func PProf(address AddressFunc) Dumper {
	return newAddressed(TransportPProf, address, func(ctx context.Context, addr string) (string, error) {
		body, err := FetchPProf(ctx, addr, "goroutine", "debug=2")
		return string(body), err
	})
}

// Gops dumps the goroutines of a Go process that runs a gops agent at the
// address address gives.
func Gops(address AddressFunc) Dumper {
	return newAddressed(TransportGops, address, func(ctx context.Context, addr string) (string, error) {
		body, err := AskGops(ctx, addr, signal.StackTrace)
		return string(body), err
	})
}

func newAddressed(transport string, address AddressFunc, dump func(context.Context, string) (string, error)) Dumper {
	if address == nil {
		panic(fmt.Sprintf("stacks: the %s dumper needs the host's address func", transport))
	}
	return addressed{transport: transport, address: address, dump: dump}
}

func (a addressed) Transport() string { return a.transport }
func (a addressed) Destructive() bool { return false }

func (a addressed) Available(pid int) error {
	if _, ok := a.address(pid); !ok {
		return fmt.Errorf("process %d has no %s address", pid, a.transport)
	}
	return nil
}

func (a addressed) Dump(ctx context.Context, pid int) (Dump, error) {
	addr, ok := a.address(pid)
	if !ok {
		return Dump{}, fmt.Errorf("process %d has no %s address", pid, a.transport)
	}
	collectedAt := time.Now().UTC()
	text, err := a.dump(ctx, addr)
	if err != nil {
		return Dump{}, err
	}
	return Dump{Format: FormatGo, Text: text, CollectedAt: collectedAt}, nil
}

// FetchPProf reads the net/http/pprof profile named profile ("goroutine",
// "heap", "profile" for CPU, ...) from the server at addr, with query.
func FetchPProf(ctx context.Context, addr, profile, query string) ([]byte, error) {
	url := "http://" + addr + "/debug/pprof/" + profile
	if query != "" {
		url += "?" + query
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("pprof %s: %w", url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("pprof %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pprof %s: %s: %s", url, response.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// AskGops sends the gops agent at addr one signal (github.com/google/gops/signal)
// and reads its whole reply.
func AskGops(ctx context.Context, addr string, request byte) ([]byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("gops %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err := conn.Write([]byte{request}); err != nil {
		return nil, fmt.Errorf("gops %s: %w", addr, err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("gops %s: %w", addr, err)
	}
	return reply, nil
}
