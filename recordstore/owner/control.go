// The owner's control socket: one JSON request and response per connection,
// answering status and nudging ingestion. Nothing depends on it for
// correctness; a producer that cannot reach it waits for the owner's poll.
package owner

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	controlProtocol = 1
	controlDeadline = 10 * time.Second

	actionStatus = "status"
	actionIngest = "ingest"

	// maxSocketPath keeps a socket address inside the shortest sun_path limit
	// of the platforms a store runs on.
	maxSocketPath = 100
)

type controlRequest struct {
	Protocol int      `json:"protocol"`
	Action   string   `json:"action"`
	IDs      []string `json:"ids,omitempty"`
	Wait     bool     `json:"wait,omitempty"`
}

type controlResponse struct {
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	State    *State   `json:"state,omitempty"`
	Ingested []string `json:"ingested,omitempty"`
}

// SocketPath is where the owner of the store configured at path serves its
// control socket: beside the store, or, when that address is too long for a
// socket, in the temp dir under a name derived from the store's path.
func SocketPath(path string) string {
	configured, err := absolute(path)
	if err != nil {
		configured = path
	}
	if socket := configured + ".sock"; len(socket) <= maxSocketPath {
		return socket
	}
	sum := sha1.Sum([]byte(configured))
	return filepath.Join(os.TempDir(), "recordstore-"+hex.EncodeToString(sum[:])[:12]+".sock")
}

// ControlHandler answers control requests: Status with the owner's state, and
// Ingest by ingesting the spool now, returning — when asked to wait — the ids
// of the batches named that the store has applied.
type ControlHandler struct {
	Status func() State
	Ingest func(ids []string, wait bool) ([]string, error)
}

// ControlServer serves a control socket until Close.
type ControlServer struct {
	path     string
	handler  ControlHandler
	listener *net.UnixListener
	accepted sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

// ServeControl serves handler on the socket at path, replacing a socket file
// nothing answers on and refusing one something does.
func ServeControl(path string, handler ControlHandler) (*ControlServer, error) {
	if _, err := os.Stat(path); err == nil {
		if conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("record store owner: control socket %s is already served", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("record store owner: remove stale control socket %s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("record store owner: inspect control socket %s: %w", path, err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("record store owner: listen on control socket %s: %w", path, err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("record store owner: restrict control socket %s: %w", path, err), listener.Close())
	}
	server := &ControlServer{path: path, handler: handler, listener: listener}
	server.accepted.Go(server.accept)
	return server, nil
}

func (s *ControlServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.accepted.Go(func() { s.serve(conn) })
	}
}

func (s *ControlServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlDeadline))
	var request controlRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(controlResponse{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}
	_ = json.NewEncoder(conn).Encode(s.handle(request))
}

func (s *ControlServer) handle(request controlRequest) controlResponse {
	if request.Protocol != controlProtocol {
		return controlResponse{Error: fmt.Sprintf("control protocol %d, this owner speaks %d", request.Protocol, controlProtocol)}
	}
	switch request.Action {
	case actionStatus:
		state := s.handler.Status()
		return controlResponse{OK: true, State: &state}
	case actionIngest:
		if s.handler.Ingest == nil {
			return controlResponse{Error: "this owner does not ingest"}
		}
		ingested, err := s.handler.Ingest(request.IDs, request.Wait)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		return controlResponse{OK: true, Ingested: ingested}
	default:
		return controlResponse{Error: fmt.Sprintf("unknown action %q", request.Action)}
	}
}

// Close stops serving and unlinks the socket if it is still this server's: a
// later owner may already serve its own at the same path.
func (s *ControlServer) Close() error {
	s.closeOnce.Do(func() {
		own := s.handler.Status().Instance
		state, err := ControlStatus(s.path, 500*time.Millisecond)
		ours := err == nil && state.Instance == own
		s.closeErr = s.listener.Close()
		s.accepted.Wait()
		if ours {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.closeErr = errors.Join(s.closeErr, err)
			}
		}
	})
	return s.closeErr
}

// ControlStatus asks the owner serving the socket at path for its state.
func ControlStatus(path string, timeout time.Duration) (State, error) {
	response, err := control(path, controlRequest{Action: actionStatus}, timeout)
	if err != nil || response.State == nil {
		return State{}, errors.Join(err, errors.New("record store owner: status carried no state"))
	}
	return *response.State, nil
}

// ControlIngest asks the owner serving the socket at path to ingest its spool
// now, and, with wait, to answer once the batches ids name are applied, with
// the ids it applied.
func ControlIngest(path string, ids []string, wait bool, timeout time.Duration) ([]string, error) {
	response, err := control(path, controlRequest{Action: actionIngest, IDs: ids, Wait: wait}, timeout)
	return response.Ingested, err
}

func control(path string, request controlRequest, timeout time.Duration) (controlResponse, error) {
	request.Protocol = controlProtocol
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return controlResponse{}, fmt.Errorf("record store owner: reach control socket %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return controlResponse{}, fmt.Errorf("record store owner: send %s: %w", request.Action, err)
	}
	var response controlResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return controlResponse{}, fmt.Errorf("record store owner: read %s answer: %w", request.Action, err)
	}
	if !response.OK {
		return response, fmt.Errorf("record store owner: %s: %s", request.Action, response.Error)
	}
	return response, nil
}
