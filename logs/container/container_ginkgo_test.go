package container

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/logs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestContainer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Container Suite")
}

var _ = Describe("Follow", func() {
	It("rejects truncated frames and invalid stream headers", func() {
		for _, data := range [][]byte{{1, 0}, {4, 0, 0, 0, 0, 0, 0, 1, 'x'}, {1, 0, 0, 0, 0, 0, 0, 2, 'x'}} {
			err := readStream(context.Background(), bytes.NewReader(data), FollowOptions{Emit: func(logs.LogLine) {}}, nil)
			Expect(err).To(HaveOccurred())
		}
	})
	It("returns the daemon system error message", func() {
		data := []byte{3, 0, 0, 0, 0, 0, 0, 12}
		data = append(data, []byte("daemon error")...)
		err := readStream(context.Background(), bytes.NewReader(data), FollowOptions{Emit: func(logs.LogLine) {}}, nil)
		Expect(err).To(MatchError(ContainSubstring("daemon error")))
	})
	It("caps long partial lines and strips carriage returns", func() {
		message := strings.Repeat("x", maxLineBytes+1) + "\r\n"
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(message)))
		var lines []logs.LogLine
		err := readStream(context.Background(), bytes.NewReader(append(header, message...)), FollowOptions{Emit: func(line logs.LogLine) { lines = append(lines, line) }}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(2))
		Expect(lines[0].Message).To(Equal(strings.Repeat("x", maxLineBytes)))
		Expect(lines[0].Labels["truncated"]).To(Equal("true"))
		Expect(lines[1].Message).To(Equal("x"))
	})
	It("does not emit an empty line when a newline follows a capped line", func() {
		message := strings.Repeat("x", maxLineBytes) + "\n"
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(message)))
		var lines []logs.LogLine
		Expect(readStream(context.Background(), bytes.NewReader(append(header, message...)), FollowOptions{Emit: func(line logs.LogLine) { lines = append(lines, line) }}, nil)).To(Succeed())
		Expect(lines).To(HaveLen(1))
	})
	It("flushes a partial line and returns cancellation while a follow stream remains open", func() {
		Expect(os.MkdirAll("../../.tmp", 0o700)).To(Succeed())
		file, err := os.CreateTemp("../../.tmp", "s")
		Expect(err).NotTo(HaveOccurred())
		socket, err := filepath.Abs(file.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())
		Expect(os.Remove(socket)).To(Succeed())
		listener, err := net.Listen("unix", socket)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, socket)
		ready := make(chan struct{})
		received := make(chan struct{})
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/json") {
				fmt.Fprint(w, `{"Id":"abc","Name":"/example","Config":{}}`)
				return
			}
			_, _ = w.Write([]byte{1, 0, 0, 0, 0, 0, 0, 7})
			_, _ = w.Write([]byte("partial"))
			_, _ = w.Write([]byte{2, 0, 0, 0, 0, 0, 0, 6})
			_, _ = w.Write([]byte("ready\n"))
			w.(http.Flusher).Flush()
			close(ready)
			<-r.Context().Done()
		}))
		server.Listener = listener
		server.Start()
		DeferCleanup(server.Close)
		GinkgoT().Setenv("DOCKER_HOST", "unix://"+socket)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var lines []logs.LogLine
		done := make(chan error, 1)
		go func() {
			done <- Follow(ctx, FollowOptions{Container: "example", Follow: true, Emit: func(line logs.LogLine) {
				lines = append(lines, line)
				if line.Message == "ready" {
					close(received)
				}
			}})
		}()
		Eventually(ready).Should(BeClosed())
		Eventually(received).Should(BeClosed())
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())
		cancel()
		Eventually(done).Should(Receive(Equal(context.Canceled)))
		Expect(lines).To(HaveLen(2))
		Expect(lines[1].Message).To(Equal("partial"))
	})
	It("reads multiplexed lines and container metadata from the configured unix socket", func() {
		Expect(os.MkdirAll("../../.tmp", 0o700)).To(Succeed())
		dir, err := os.MkdirTemp("../../.tmp", "s")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, dir)
		socket, err := filepath.Abs(filepath.Join(dir, "s"))
		Expect(err).NotTo(HaveOccurred())
		listener, err := net.Listen("unix", socket)
		Expect(err).NotTo(HaveOccurred())
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/containers/example/json":
				fmt.Fprint(w, `{"Id":"abc123","Name":"/example","Config":{"Labels":{"team":"ops"},"Tty":false}}`)
			case "/containers/example/logs":
				Expect(r.URL.Query().Get("since")).To(Equal("1700000000"))
				Expect(r.URL.Query().Get("tail")).To(Equal("10"))
				Expect(r.URL.Query().Get("follow")).To(Equal("true"))
				for _, frame := range []struct {
					stream byte
					text   string
				}{{1, "hello\npartial"}, {2, "error\n"}, {1, " line\n"}} {
					header := make([]byte, 8)
					header[0] = frame.stream
					binary.BigEndian.PutUint32(header[4:], uint32(len(frame.text)))
					_, _ = w.Write(header)
					_, _ = w.Write([]byte(frame.text))
				}
			default:
				http.NotFound(w, r)
			}
		}))
		server.Listener = listener
		server.Start()
		DeferCleanup(server.Close)
		GinkgoT().Setenv("DOCKER_HOST", "unix://"+socket)
		var lines []logs.LogLine
		err = Follow(context.Background(), FollowOptions{Container: "example", Host: "node-a", Runtime: "podman", Since: time.Unix(1700000000, 0), Tail: "10", Follow: true, Emit: func(line logs.LogLine) { lines = append(lines, line) }})
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(3))
		Expect([]string{lines[0].Source, lines[0].Message, lines[1].Source, lines[1].Message, lines[2].Source, lines[2].Message}).To(Equal([]string{"stdout", "hello", "stderr", "error", "stdout", "partial line"}))
		Expect(lines[0].Host).To(Equal("node-a"))
		Expect(lines[0].Labels).To(Equal(map[string]string{"team": "ops", "container.id": "abc123", "container.name": "example", "container.runtime": "podman"}))
	})
})
