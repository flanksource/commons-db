package httpproxy

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/flanksource/commons/properties"
)

// Mode is how much of the traffic through the proxy a capture sees.
type Mode string

const (
	// ModeConnect records each CONNECT tunnel by its host, duration and byte
	// counts, its payload left encrypted.
	ModeConnect Mode = "connect"
	// ModeMITM terminates TLS with an ephemeral CA the capture exports for its
	// processes to trust, recording each decrypted exchange in full.
	ModeMITM Mode = "mitm"
)

// defaultBodies is how much of each body a capture keeps unless its params
// say otherwise: bodies are the likeliest place for both a large payload and
// a secret, so the default stays small enough to read.
const defaultBodies = "256KiB"

// maxBodies is the most of a body a capture may keep.
const maxBodies = 4 << 20

// Params configure the recording proxy a capture's processes are pointed at.
// Plain (non-TLS) exchanges are recorded in full in either mode.
type Params struct {
	Mode Mode `json:"mode,omitempty" clicky:"title=Mode"`
	// Hosts are host globs (path.Match), without a port, limiting what is
	// recorded and, under mitm, what is decrypted; empty records every host.
	Hosts []string `json:"hosts,omitempty" clicky:"title=Hosts"`
	// Paths are URL-path globs (path.Match) limiting which exchanges are
	// recorded; empty records every path. A tunnel has no path to match, so
	// it is recorded on Hosts alone.
	Paths []string `json:"paths,omitempty" clicky:"title=Paths"`
	// Bodies caps how much of each body is kept, as a size such as 64KiB or
	// 4MiB, at most 4MiB; 0 keeps none.
	Bodies string `json:"bodies,omitempty" clicky:"title=Bodies"`
	// Redact names headers to blank on top of the built-in credential headers,
	// which are always blanked.
	Redact []string `json:"redact,omitempty" clicky:"title=Redact headers"`
	// NoProxy are host:port addresses the processes reach directly, such as a
	// server the host started, beside the proxy itself.
	NoProxy []string `json:"noProxy,omitempty" clicky:"title=No proxy"`
	// UpstreamCA is a PEM file of authorities the proxy trusts beside the
	// system roots when, under mitm, it connects to the real server, such as a
	// local test server's own certificate. It is read when the capture starts.
	UpstreamCA string `json:"upstreamCA,omitempty" clicky:"title=Upstream CA file"`
}

func (p Params) Validate() error {
	if p.Mode != ModeConnect && p.Mode != ModeMITM {
		return fmt.Errorf("mode %q is neither %q nor %q", p.Mode, ModeConnect, ModeMITM)
	}
	if _, err := p.bodyLimit(); err != nil {
		return err
	}
	if p.UpstreamCA != "" && strings.TrimSpace(p.UpstreamCA) == "" {
		return errors.New("upstreamCA must not be blank")
	}
	if p.UpstreamCA != "" && p.Mode != ModeMITM {
		return fmt.Errorf("upstreamCA applies only to mode %q: a %q tunnel is verified by its client", ModeMITM, p.Mode)
	}
	return errors.Join(globs("hosts", p.Hosts), globs("paths", p.Paths), names("redact", p.Redact), names("noProxy", p.NoProxy))
}

// bodyLimit is how many bytes of each body the capture keeps.
func (p Params) bodyLimit() (int64, error) {
	limit, err := properties.ParseBytes(p.Bodies)
	if err != nil {
		return 0, fmt.Errorf("bodies: %w", err)
	}
	if limit < 0 || limit > maxBodies {
		return 0, fmt.Errorf("bodies %q is not between 0 and 4MiB", p.Bodies)
	}
	return limit, nil
}

func globs(field string, patterns []string) error {
	for _, pattern := range patterns {
		if _, err := path.Match(pattern, ""); err != nil || strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("%s: %q is not a glob", field, pattern)
		}
	}
	return nil
}

func names(field string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: a name must not be blank", field)
		}
	}
	return nil
}
