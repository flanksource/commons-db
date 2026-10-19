package stacks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/tracing/process"
)

// The formats a Dump is in.
const (
	// FormatGo is a Go goroutine dump, as runtime/pprof's goroutine profile
	// writes it at debug=2 and a Go process prints it as SIGQUIT kills it.
	FormatGo = "go"
	// FormatNative is an OS stack sampler's dump of a process's threads.
	FormatNative = "native"
)

// Dumper dumps the stacks of a process over one transport: it is the stacks
// kind's transport, which a host provides.
type Dumper interface {
	// Transport names it, such as "inprocess", "native", "pprof", "gops" or
	// "sigquit".
	Transport() string
	// Destructive reports whether a dump kills the process, as SIGQUIT does.
	Destructive() bool
	// Available is nil when this dumper can dump pid, or why it cannot.
	Available(pid int) error
	Dump(ctx context.Context, pid int) (Dump, error)
}

// Dump is a process's stacks as one dump took them.
type Dump struct {
	Format      string    `json:"format"`
	Text        string    `json:"text"`
	CollectedAt time.Time `json:"collectedAt"`
}

// Validate refuses a dump that names no format it is parsed with, holds no
// text or has no time.
func (d Dump) Validate() error {
	var errs []error
	if d.Format != FormatGo && d.Format != FormatNative {
		errs = append(errs, fmt.Errorf("dump format %q: expected %q or %q", d.Format, FormatGo, FormatNative))
	}
	if d.Text == "" {
		errs = append(errs, errors.New("dump has no text"))
	}
	if d.CollectedAt.IsZero() {
		errs = append(errs, errors.New("dump needs the collectedAt it was dumped at"))
	}
	return errors.Join(errs...)
}

// AddressFunc is the address of pid's pprof HTTP server or gops agent, as
// host:port, when it has one.
type AddressFunc func(pid int) (addr string, ok bool)

// Dumpers is the registry of the transports a host's stacks are dumped with,
// in the order a dump that names none prefers them. One Dumpers is shared,
// like a process.Sampler, by the stacks kind and the process_tree kind that
// lists each process's transports.
type Dumpers struct {
	mu      sync.RWMutex
	dumpers []Dumper
}

// NewDumpers registers dumpers, in order.
func NewDumpers(dumpers ...Dumper) (*Dumpers, error) {
	registry := &Dumpers{}
	for _, dumper := range dumpers {
		if err := registry.Register(dumper); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register adds dumper after those registered, refusing a transport twice.
func (d *Dumpers) Register(dumper Dumper) error {
	if dumper == nil || dumper.Transport() == "" {
		return errors.New("a stack dumper needs a transport name")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, registered := range d.dumpers {
		if registered.Transport() == dumper.Transport() {
			return fmt.Errorf("stack transport %q is registered twice", dumper.Transport())
		}
	}
	d.dumpers = append(d.dumpers, dumper)
	return nil
}

// Transports are the transports that can dump pid, in order.
func (d *Dumpers) Transports(pid int) []process.StackTransport {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var transports []process.StackTransport
	for _, dumper := range d.dumpers {
		if dumper.Available(pid) == nil {
			transports = append(transports, process.StackTransport{Transport: dumper.Transport(), Destructive: dumper.Destructive()})
		}
	}
	return transports
}

// pick is the dumper named transport, which must be able to dump pid, or the
// first non-destructive one that can when transport is empty. A transport
// that is unknown or cannot dump pid is errNamed; none that can is errNone.
func (d *Dumpers) pick(pid int, transport string) (Dumper, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var why []error
	for _, dumper := range d.dumpers {
		switch {
		case transport != "" && dumper.Transport() != transport:
		case transport == "" && dumper.Destructive():
		default:
			err := dumper.Available(pid)
			if err == nil {
				return dumper, nil
			}
			why = append(why, fmt.Errorf("%s: %w", dumper.Transport(), err))
		}
	}
	if transport != "" && len(why) == 0 {
		return nil, fmt.Errorf("%w: unknown stack transport %q", errNamed, transport)
	}
	if transport != "" {
		return nil, fmt.Errorf("%w: %w", errNamed, errors.Join(why...))
	}
	return nil, fmt.Errorf("%w for process %d: %w", errNone, pid, errors.Join(why...))
}

var (
	errNamed = errors.New("the named stack transport cannot dump")
	errNone  = errors.New("no stack transport can dump")
)
