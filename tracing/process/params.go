// Package process holds the process trace kinds: process_tree records each
// process under a root as it starts, changes and exits, and process_samples
// records the CPU, memory, disk IO and armed facets of every one of them at
// each tick.
package process

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
)

// MinInterval is the shortest tick a capture takes: each tick lists every
// process on the host.
const MinInterval = 250 * time.Millisecond

const defaultInterval = time.Second

// Params choose the process tree a capture follows and how often it looks.
type Params struct {
	// RootPID is the process whose tree is captured; zero is the host's own.
	RootPID int `json:"rootPid,omitempty" clicky:"title=Root PID"`
	// Interval, a duration such as 1s, is how often the tree is sampled.
	// Empty is a second.
	Interval string `json:"interval,omitempty" clicky:"title=Interval"`
}

func (p Params) Validate() error {
	if p.RootPID < 0 {
		return fmt.Errorf("rootPid %d is not a process id", p.RootPID)
	}
	_, err := p.interval()
	return err
}

func (p Params) interval() (time.Duration, error) {
	if p.Interval == "" {
		return defaultInterval, nil
	}
	interval, err := time.ParseDuration(p.Interval)
	if err != nil {
		return 0, fmt.Errorf("interval %q is not a duration such as 1s", p.Interval)
	}
	if interval < MinInterval {
		return 0, fmt.Errorf("interval %s is shorter than %s", interval, MinInterval)
	}
	return interval, nil
}

// root is the process the capture follows.
func (p Params) root() int {
	if p.RootPID == 0 {
		return os.Getpid()
	}
	return p.RootPID
}

// TreeParams are the params of process_tree: the tree, and which environment
// variables each process's node is read with.
type TreeParams struct {
	Params
	// Environment reads each process's environment once, as its node is
	// created, limited to these keys and prefixes; empty ones read all of it.
	// Absent reads none.
	Environment *EnvironmentParams `json:"environment,omitempty" clicky:"title=Environment"`
}

// EnvironmentParams name the environment variables a node is read with.
type EnvironmentParams struct {
	Keys     []string `json:"keys,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
}

func (p EnvironmentParams) options() clickyprocess.EnvironmentOptions {
	return clickyprocess.EnvironmentOptions{Keys: p.Keys, Prefixes: p.Prefixes}
}

// The facets process_samples reads of each process's open descriptors.
const (
	// FacetOpenFiles are the descriptors that are no socket.
	FacetOpenFiles = "openFiles"
	// FacetSockets are the internet and unix-domain sockets.
	FacetSockets = "sockets"
	// FacetPorts are the sockets listening on a port.
	FacetPorts = "ports"
	// FacetConnections are the TCP and UDP sockets with a peer.
	FacetConnections = "connections"
)

// Facets are every facet a capture can arm.
var Facets = []string{FacetOpenFiles, FacetSockets, FacetPorts, FacetConnections}

// SampleParams are the params of process_samples: the tree, and the facets
// read of every process at each tick.
type SampleParams struct {
	Params
	// Facets are read of every process at each tick, with one lsof call for
	// the whole tree: each one's count every tick, its list when it changed.
	Facets []string `json:"facets,omitempty" clicky:"title=Facets"`
}

func (p SampleParams) Validate() error {
	var errs []error
	for index, facet := range p.Facets {
		switch {
		case !slices.Contains(Facets, facet):
			errs = append(errs, fmt.Errorf("facet %q: expected one of %v", facet, Facets))
		case slices.Contains(p.Facets[:index], facet):
			errs = append(errs, fmt.Errorf("facet %q twice", facet))
		}
	}
	return errors.Join(append(errs, p.Params.Validate())...)
}

func (p SampleParams) armed(facet string) bool { return slices.Contains(p.Facets, facet) }

// errRootGone ends a capture whose root has exited.
var errRootGone = errors.New("root process exited")
