package workspace

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/flanksource/commons-db/context"
)

// GateSpec selects a registered gate by name, with its configuration.
type GateSpec struct {
	Name   string         `json:"name" yaml:"name"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// GateFactory builds a gate from its configuration; it must reject keys it
// does not understand.
type GateFactory func(config map[string]any) (Gate, error)

const (
	SecretNamesGateName = "secret-names"
	MaxSizeGateName     = "max-size"
)

var (
	gatesMu       sync.RWMutex
	gateFactories = map[string]GateFactory{
		SecretNamesGateName: newSecretNamesGate,
		MaxSizeGateName:     newMaxSizeGate,
	}
)

// RegisterGate makes a host gate (a linter, a secret scanner) selectable by
// name. It panics on a duplicate or reserved name, as a registration mistake.
func RegisterGate(name string, factory GateFactory) {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	if name == "" || name == ExcludesGateName {
		panic(fmt.Sprintf("workspace: gate name %q is reserved", name))
	}
	if _, ok := gateFactories[name]; ok {
		panic(fmt.Sprintf("workspace: gate %q is already registered", name))
	}
	gateFactories[name] = factory
}

// ResolveGates builds the configured gates. No spec means no gates beyond the
// built-in excludes gate; an unknown name fails.
func ResolveGates(specs []GateSpec) ([]Gate, error) {
	gatesMu.RLock()
	defer gatesMu.RUnlock()
	gates := make([]Gate, 0, len(specs))
	for _, spec := range specs {
		factory, ok := gateFactories[spec.Name]
		if !ok {
			return nil, fmt.Errorf("workspace: unknown gate %q (registered: %s)", spec.Name, strings.Join(registeredGates(), ", "))
		}
		gate, err := factory(spec.Config)
		if err != nil {
			return nil, fmt.Errorf("workspace: gate %q: %w", spec.Name, err)
		}
		gates = append(gates, gate)
	}
	return gates, nil
}

func registeredGates() []string {
	names := make([]string, 0, len(gateFactories))
	for name := range gateFactories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func rejectUnknownKeys(config map[string]any, known ...string) error {
	for key := range config {
		if !slices.Contains(known, key) {
			return fmt.Errorf("unknown config key %q", key)
		}
	}
	return nil
}

// secretBaseNames are file names that carry credentials often enough that
// committing one unattended is never the intended outcome.
var secretBaseNames = map[string]bool{
	".env":                 true,
	".netrc":               true,
	".npmrc":               true,
	".pypirc":              true,
	"credentials.json":     true,
	"id_rsa":               true,
	"id_ed25519":           true,
	"service-account.json": true,
}

// secretSuffixes are extensions whose contents are keys by definition.
var secretSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore"}

// LooksSecret reports whether a repo-relative path names a credential file, by
// base name (including the .env.production family) or by extension.
func LooksSecret(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if secretBaseNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

type secretNamesGate struct{}

func newSecretNamesGate(config map[string]any) (Gate, error) {
	return secretNamesGate{}, rejectUnknownKeys(config)
}

func (secretNamesGate) Name() string { return SecretNamesGateName }

func (secretNamesGate) Check(_ context.Context, _ *Workspace, c Check) error {
	var findings []string
	for _, path := range gatedPaths(c) {
		if LooksSecret(path) {
			findings = append(findings, path)
		}
	}
	if len(findings) > 0 {
		return &GateError{Gate: SecretNamesGateName, Phase: c.Phase, Findings: findings}
	}
	return nil
}

// gatedPaths is what a content gate inspects: a commit's added or modified
// paths (deleting a credential file is fine), or every path a push's history
// touched (a file added then removed still travels).
func gatedPaths(c Check) []string {
	if c.Phase != PhaseCommit {
		return c.Paths
	}
	var paths []string
	for _, change := range c.Changes {
		if change.Status != StatusDeleted {
			paths = append(paths, change.Path)
		}
	}
	return paths
}

type maxSizeGate struct{ bytes int64 }

func newMaxSizeGate(config map[string]any) (Gate, error) {
	if err := rejectUnknownKeys(config, "bytes"); err != nil {
		return nil, err
	}
	bytes, err := positiveInt(config["bytes"])
	if err != nil {
		return nil, fmt.Errorf("config.bytes: %w", err)
	}
	return maxSizeGate{bytes: bytes}, nil
}

func (maxSizeGate) Name() string { return MaxSizeGateName }

func (g maxSizeGate) Check(ctx context.Context, ws *Workspace, c Check) error {
	blobs, err := ws.blobSizes(ctx, c)
	if err != nil {
		return err
	}
	var findings []string
	for _, blob := range blobs {
		if blob.size > g.bytes {
			findings = append(findings, fmt.Sprintf("%s (%d bytes)", blob.path, blob.size))
		}
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		return &GateError{Gate: MaxSizeGateName, Phase: c.Phase, Findings: findings}
	}
	return nil
}

type blobSize struct {
	path string
	size int64
}

// blobSizes lists the blobs a check would carry: the staged blobs of a
// commit's gated paths, or every blob the push's range introduces.
func (w *Workspace) blobSizes(ctx context.Context, c Check) ([]blobSize, error) {
	var objects []byte
	var err error
	if c.Phase == PhaseCommit {
		paths := gatedPaths(c)
		if len(paths) == 0 {
			return nil, nil
		}
		// ls-files takes no --pathspec-from-file; literal pathspecs after "--".
		objects, err = gitCmd{Dir: w.Dir, Env: []string{"GIT_LITERAL_PATHSPECS=1"}}.
			output(ctx, append([]string{"ls-files", "-s", "-z", "--"}, paths...)...)
		if err != nil {
			return nil, err
		}
		objects = []byte(stageEntriesToObjects(splitNUL(objects)))
	} else {
		args := []string{"rev-list", "--objects", c.To}
		if c.From != "" {
			args = append(args, "^"+c.From)
		}
		if objects, err = gitOutput(ctx, w.Dir, args...); err != nil {
			return nil, err
		}
	}
	out, err := gitCmd{Dir: w.Dir, Stdin: objects}.output(ctx, "cat-file", "--batch-check=%(objecttype) %(objectsize) %(rest)")
	if err != nil {
		return nil, err
	}
	var blobs []blobSize
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 || fields[0] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("workspace: cat-file size %q: %w", line, err)
		}
		blobs = append(blobs, blobSize{path: fields[2], size: size})
	}
	return blobs, nil
}

// stageEntriesToObjects turns `ls-files -s -z` records ("<mode> <sha> <stage>\t<path>")
// into cat-file input lines ("<sha> <path>").
func stageEntriesToObjects(entries []string) string {
	var b strings.Builder
	for _, entry := range entries {
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			continue
		}
		b.WriteString(fields[1] + " " + path + "\n")
	}
	return b.String()
}

func positiveInt(value any) (int64, error) {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		n = int64(v)
		if float64(n) != v {
			return 0, fmt.Errorf("must be a whole number, got %v", v)
		}
	case nil:
		return 0, fmt.Errorf("is required")
	default:
		return 0, fmt.Errorf("must be a number, got %T", value)
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return n, nil
}
