package recordstore

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons/duration"
	"github.com/flanksource/commons/properties"
)

// BackendKind names a backend a store's streams can live in.
type BackendKind string

const (
	BackendKV     BackendKind = "kv"
	BackendSQLite BackendKind = "sqlite"
	BackendNDJSON BackendKind = "ndjson"
)

var backendKinds = []BackendKind{BackendKV, BackendSQLite, BackendNDJSON}

// Settings say where a store's streams live and for how long. A store reads
// them from commons/properties (-P, env, a properties file) under a prefix of
// its own:
//
//	<prefix>.backend             kv | sqlite | ndjson; unset lets Resolve pick
//	<prefix>.dir                 where sqlite files and ndjson streams live
//	<prefix>.ttl                 how long a stream is kept, e.g. 30d or 36h
//	<prefix>.ndjson.maxBytes     one ndjson stream's cap, e.g. 256MiB
//	<prefix>.ndjson.keepStreams  ndjson streams kept per kind
type Settings struct {
	// Prefix is the property prefix the settings were read under; errors about
	// a setting name the key under it.
	Prefix string

	// Backend is the backend asked for, or empty for the caller's default.
	Backend           BackendKind
	Dir               string
	TTL               time.Duration
	NDJSONMaxBytes    int64
	NDJSONKeepStreams int
}

// ReadSettings reads prefix's properties over defaults. A key that is unset
// keeps its default; a key that is set but unusable is an error naming it,
// never a silent fallback to the default.
func ReadSettings(prefix string, defaults Settings) (Settings, error) {
	if strings.TrimSpace(prefix) == "" {
		return Settings{}, errors.New("record store settings need a property prefix")
	}
	settings := defaults
	settings.Prefix = prefix
	read := settings.property
	if raw := read("backend"); raw != "" {
		settings.Backend = BackendKind(raw)
		if err := settings.validateBackend(); err != nil {
			return Settings{}, err
		}
	}
	if raw := read("dir"); raw != "" {
		settings.Dir = raw
	}
	if raw := read("ttl"); raw != "" {
		parsed, err := duration.ParseDuration(raw)
		if err != nil || time.Duration(parsed) <= 0 {
			return Settings{}, fmt.Errorf("%s must be a positive duration such as 30d or 36h, got %q", settings.key("ttl"), raw)
		}
		settings.TTL = time.Duration(parsed)
	}
	if raw := read("ndjson.maxBytes"); raw != "" {
		parsed, err := properties.ParseBytes(raw)
		if err != nil || parsed <= 0 {
			return Settings{}, fmt.Errorf("%s must be a positive size such as 256MiB, got %q", settings.key("ndjson.maxBytes"), raw)
		}
		settings.NDJSONMaxBytes = parsed
	}
	if raw := read("ndjson.keepStreams"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return Settings{}, fmt.Errorf("%s must be a positive integer, got %q", settings.key("ndjson.keepStreams"), raw)
		}
		settings.NDJSONKeepStreams = parsed
	}
	return settings, nil
}

// Resolve is the backend a store writes to. An explicit backend is used as
// asked; without one, kv wherever the caller has a kv store — the store every
// process can share — and fallback, a local file, where it has none.
//
// hasKV is a fact about one caller, which may differ per tenant, so it is
// resolved per call. An explicit kv where there is no kv store is an error:
// writing somewhere else would lose the one property kv was asked for.
func (s Settings) Resolve(hasKV bool, fallback BackendKind) (BackendKind, error) {
	if fallback != BackendSQLite && fallback != BackendNDJSON {
		return "", fmt.Errorf("record store fallback must be %s or %s, got %q", BackendSQLite, BackendNDJSON, fallback)
	}
	if err := s.validateBackend(); err != nil {
		return "", err
	}
	switch {
	case s.Backend == BackendKV && !hasKV:
		return "", fmt.Errorf("%s is kv, but there is no kv store to write to", s.key("backend"))
	case s.Backend != "":
		return s.Backend, nil
	case hasKV:
		return BackendKV, nil
	default:
		return fallback, nil
	}
}

func (s Settings) validateBackend() error {
	if s.Backend != "" && !slices.Contains(backendKinds, s.Backend) {
		return fmt.Errorf("%s must be one of %v, got %q", s.key("backend"), backendKinds, s.Backend)
	}
	return nil
}

func (s Settings) key(name string) string { return s.Prefix + "." + name }

func (s Settings) property(name string) string {
	return strings.TrimSpace(properties.Get(s.key(name)))
}
