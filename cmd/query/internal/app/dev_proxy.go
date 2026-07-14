package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// startViteDevProxy launches Vite in cmd/query/www on an OS-assigned free port
// and returns an HTTP handler that reverse-proxies to it. apiHost and apiPort
// point back at this Go server so Vite's own proxy routes /api and /health to
// the same process that spawned it (QUERY_API_URL handshake) rather than a
// hardcoded :8080. The returned cleanup terminates the Vite process group; the
// caller MUST invoke it on every shutdown path.
func startViteDevProxy(ctx context.Context, apiHost string, apiPort int) (http.Handler, func(), error) {
	vitePort, err := pickFreePort()
	if err != nil {
		return nil, nil, fmt.Errorf("allocate vite dev port: %w", err)
	}
	viteAddr := fmt.Sprintf("127.0.0.1:%d", vitePort)

	// `--strictPort` makes the port we picked the only one Vite will accept — if
	// anything raced into it between the listen probe and exec, Vite errors out
	// loudly instead of silently falling back to another port.
	cmd := exec.CommandContext(ctx, "pnpm", "exec", "vite",
		"--strictPort",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", vitePort),
	)
	cmd.Dir = "cmd/query/www"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Raise Node's HTTP header size cap so Vite's dev server accepts browser
	// headers forwarded through our reverse proxy. The 8 KB default is often
	// exceeded by modern Chrome + dev cookies, producing opaque 431 responses.
	// https://vite.dev/guide/troubleshooting.html#_431-request-header-fields-too-large
	const headerFlag = "--max-http-header-size=65536"
	nodeOpts := os.Getenv("NODE_OPTIONS")
	if !strings.Contains(nodeOpts, "--max-http-header-size") {
		if nodeOpts != "" {
			nodeOpts += " "
		}
		nodeOpts += headerFlag
	}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("QUERY_API_URL=http://%s:%d", apiHost, apiPort),
		"NODE_OPTIONS="+nodeOpts,
	)

	fmt.Printf("🔧 starting Vite dev server (pnpm exec vite --port %d) in cmd/query/www, API → http://%s:%d\n", vitePort, apiHost, apiPort)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start vite: %w", err)
	}

	if err := waitForPort(viteAddr, 30*time.Second); err != nil {
		stopProcessGroup(cmd)
		return nil, nil, fmt.Errorf("vite dev server did not become reachable on %s: %w", viteAddr, err)
	}
	fmt.Printf("✅ Vite dev server ready on http://%s\n", viteAddr)

	target, err := url.Parse(fmt.Sprintf("http://%s", viteAddr))
	if err != nil {
		stopProcessGroup(cmd)
		return nil, nil, fmt.Errorf("parse vite proxy target: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	cleanup := func() {
		fmt.Printf("🛑 stopping Vite dev server (pid=%d)\n", cmd.Process.Pid)
		stopProcessGroup(cmd)
	}
	return proxy, cleanup, nil
}

// pickFreePort asks the kernel for an unused TCP port on 127.0.0.1, closes the
// probe listener, and returns the port number. There is an unavoidable race
// window between close and Vite binding it, but it is far smaller than the
// alternative of a hardcoded port colliding with another Vite instance.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// stopProcessGroup sends SIGTERM to the entire process group of cmd, waits up to
// 3 seconds for graceful exit, then SIGKILLs. Must be called only after a
// successful cmd.Start().
func stopProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// waitForPort blocks until addr accepts a TCP connection or timeout elapses.
func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("port %s did not become reachable within %s", addr, timeout)
}
