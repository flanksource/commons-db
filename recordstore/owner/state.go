// Package owner elects one process to write a record store, the owner, by a
// file lock beside the store's configured path. Every other process reads the
// store and hands its writes to the owner through the store's spool, and the
// first of them to take the lock after the owner exits becomes the owner.
//
// Beside a store configured at P the package keeps P.lock (the lock, the only
// truth about who owns the store), P.owner.json (the owner's advisory state),
// P.sock (the owner's control socket, which only speeds things up) and P.spool
// (the spool, see recordstore/spool).
package owner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Phase is where the owner is in its life.
type Phase string

const (
	PhaseStarting Phase = "starting"
	PhaseReady    Phase = "ready"
	PhaseDraining Phase = "draining"
)

// SpoolState is what a producer needs to publish batches the owner reads.
type SpoolState struct {
	Dir            string   `json:"dir"`
	ManifestFormat int      `json:"manifestFormat"`
	Formats        []string `json:"formats"`
}

// State is the owner's advisory state, published beside the store. The lock,
// not this file, says who owns the store; a reader uses it to find the owner's
// socket and spool, and to name the owner in errors.
type State struct {
	Instance       string      `json:"instance"`
	PID            int         `json:"pid"`
	Host           string      `json:"host,omitempty"`
	Build          string      `json:"build,omitempty"`
	Phase          Phase       `json:"phase"`
	StartedAt      time.Time   `json:"startedAt"`
	HeartbeatAt    time.Time   `json:"heartbeatAt"`
	Store          string      `json:"store,omitempty"`
	CatalogVersion int         `json:"catalogVersion,omitempty"`
	Spool          *SpoolState `json:"spool,omitempty"`
	Socket         string      `json:"socket,omitempty"`
	Backlog        int         `json:"backlog"`
	Failed         int         `json:"failed"`
	LastError      string      `json:"lastError,omitempty"`
}

// failpoint marks the moments a crash would be hardest to recover from, for a
// test binary to crash or pause at; it does nothing otherwise.
var failpoint = func(string) {}

// newInstance names one process's hold on a store.
func newInstance() string { return uuid.NewString() }

// absolute is the configured path every file beside it is derived from.
func absolute(configured string) (string, error) {
	path, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("record store owner: resolve %s: %w", configured, err)
	}
	return path, nil
}

func statePath(configured string) string { return configured + ".owner.json" }

func lockPath(configured string) string { return configured + ".lock" }

func spoolPath(configured string) string { return configured + ".spool" }

// ReadState reads the state the owner of the store configured at path
// published, reporting false when no owner published one.
func ReadState(path string) (State, bool, error) {
	configured, err := absolute(path)
	if err != nil {
		return State{}, false, err
	}
	encoded, err := os.ReadFile(statePath(configured))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("record store owner: read state of %s: %w", configured, err)
	}
	var state State
	if err := json.Unmarshal(encoded, &state); err != nil {
		return State{}, false, fmt.Errorf("record store owner: decode state of %s: %w", configured, err)
	}
	return state, true, nil
}

// writeState publishes state for the store at configured through a temp file
// of its own, synced and renamed into place, so a reader never sees half of
// it.
func writeState(configured string, state State) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	target := statePath(configured)
	temporary, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*")
	if err != nil {
		return fmt.Errorf("record store owner: write state of %s: %w", configured, err)
	}
	_, err = temporary.Write(encoded)
	err = errors.Join(err, temporary.Sync(), temporary.Close())
	if err == nil {
		err = os.Rename(temporary.Name(), target)
	}
	if err != nil {
		_ = os.Remove(temporary.Name())
		return fmt.Errorf("record store owner: write state of %s: %w", configured, err)
	}
	return nil
}

// removeState removes the published state while it is still instance's: a
// later owner's state is not this process's to remove.
func removeState(configured, instance string) error {
	state, found, err := ReadState(configured)
	if err != nil || !found || state.Instance != instance {
		return err
	}
	if err := os.Remove(statePath(configured)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("record store owner: remove state of %s: %w", configured, err)
	}
	return nil
}

// processState is a fresh state naming this process as instance.
func processState(instance, build string, phase Phase) State {
	host, _ := os.Hostname()
	now := time.Now().UTC()
	return State{Instance: instance, PID: os.Getpid(), Host: host, Build: build, Phase: phase, StartedAt: now, HeartbeatAt: now}
}
