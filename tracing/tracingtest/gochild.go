package tracingtest

import (
	"bufio"
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// GoChildBinary builds the Go child the process kinds' specs observe
// (testdata/gochild) into dir, returning its path; a suite builds it once.
func GoChildBinary(dir string) string {
	// The package's directory, which runtime.Caller does not give under
	// -trimpath.
	source, err := exec.Command("go", "list", "-f", "{{.Dir}}", "github.com/flanksource/commons-db/tracing/tracingtest").Output()
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	binary := filepath.Join(dir, "gochild")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(strings.TrimSpace(string(source)), "testdata", "gochild")
	output, err := build.CombinedOutput()
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), string(output))
	return binary
}

// GoChild is a running Go child: what it announced, by name, and its stdin.
type GoChild struct {
	Cmd       *exec.Cmd
	PID       int
	Announced map[string]string
	Stdin     io.WriteCloser
	// Stderr is what the child wrote to stderr, to read once it exited.
	Stderr *bytes.Buffer
	lines  chan string
	exited chan struct{}
}

// Exited is closed once the child exited, and its Stderr is whole.
func (c *GoChild) Exited() <-chan struct{} { return c.exited }

// StartGoChild runs binary with args in its own process group, which the spec
// kills when it ends, and waits for it to announce it is ready.
func StartGoChild(binary string, args ...string) *GoChild {
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	stdin, err := cmd.StdinPipe()
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	gomega.Expect(cmd.Start()).To(gomega.Succeed())
	child := &GoChild{Cmd: cmd, PID: cmd.Process.Pid, Announced: map[string]string{}, Stdin: stdin, Stderr: stderr, lines: make(chan string, 16)}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	child.exited = exited
	ginkgo.DeferCleanup(func() {
		_ = syscall.Kill(-child.PID, syscall.SIGKILL)
		<-exited
	})
	go func() {
		defer close(child.lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			child.lines <- scanner.Text()
		}
	}()
	child.Await("ready")
	return child
}

// Await waits for the child to announce name, and returns its value.
func (c *GoChild) Await(name string) string {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-c.lines:
			gomega.Expect(ok).To(gomega.BeTrue(), "the child exited before it announced %s", name)
			announced, value, _ := strings.Cut(line, " ")
			c.Announced[announced] = value
			if announced == name {
				return value
			}
		case <-timeout:
			ginkgo.Fail("the child never announced " + name)
		}
	}
}
