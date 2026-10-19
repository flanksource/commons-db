// gochild is the Go process the trace kinds' specs observe: it exposes what a
// flag asks for, announces each on stdout as "<name> <value>", then "ready",
// and runs until it is killed or its stdin closes. Each "open <path>" line on
// stdin opens one more file.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/google/gops/agent"
)

func main() {
	serve := flag.Bool("pprof", false, "serve net/http/pprof")
	gops := flag.String("gops", "", "run a gops agent, keeping its port file in this dir")
	cpuProfile := flag.String("cpuprofile", "", "burn CPU into this CPU profile, then exit")
	hold := flag.String("hold", "", "hold this file open")
	listen := flag.Bool("listen", false, "listen on a TCP port and dial it")
	flag.Parse()

	if *cpuProfile != "" {
		must(profileCPU(*cpuProfile))
		return
	}
	go parkedForever(make(chan struct{}))
	var held []any
	if *serve {
		listener := mustListen()
		go func() { must(http.Serve(listener, nil)) }()
		announce("pprof", listener.Addr().String())
	}
	if *gops != "" {
		must(agent.Listen(agent.Options{Addr: "127.0.0.1:0", ConfigDir: *gops}))
		port, err := os.ReadFile(filepath.Join(*gops, fmt.Sprint(os.Getpid())))
		must(err)
		announce("gops", "127.0.0.1:"+string(port))
	}
	if *hold != "" {
		file, err := os.Create(*hold)
		must(err)
		held = append(held, file)
	}
	if *listen {
		held = append(held, connectToSelf()...)
	}
	announce("ready", fmt.Sprint(len(held)))
	commands(held)
}

// commands opens the file each "open <path>" line names until stdin closes.
func commands(held []any) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if path, ok := strings.CutPrefix(scanner.Text(), "open "); ok {
			file, err := os.Create(path)
			must(err)
			held = append(held, file)
			announce("opened", path)
		}
	}
}

// connectToSelf listens on a port and holds a connection dialed to it.
func connectToSelf() []any {
	listener := mustListen()
	accepted := make(chan net.Conn)
	go func() {
		conn, err := listener.Accept()
		must(err)
		accepted <- conn
	}()
	dialed, err := net.Dial("tcp", listener.Addr().String())
	must(err)
	announce("listen", listener.Addr().String())
	return []any{listener, dialed, <-accepted}
}

func profileCPU(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := pprof.StartCPUProfile(file); err != nil {
		return err
	}
	burnCPU(500 * time.Millisecond)
	pprof.StopCPUProfile()
	return nil
}

//go:noinline
func burnCPU(d time.Duration) {
	sum := 0
	for end := time.Now().Add(d); time.Now().Before(end); {
		for i := range 10000 {
			sum += i * i
		}
	}
	fmt.Fprintln(os.Stderr, sum)
}

//go:noinline
func parkedForever(never chan struct{}) { <-never }

func mustListen() net.Listener {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	return listener
}

func announce(name, value string) { fmt.Printf("%s %s\n", name, value) }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
