package profiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
)

// The explorer builds itself out of this document — nav, filter controls,
// forms, chat tools — and never reads an operation's responses, which are more
// than half its bytes. Asking for this media type serves the same document with
// those schemas reduced to a stub; everything else, Scalar and generated
// clients included, keeps the complete one. A stub rather than an omission
// because OpenAPI 3.0 requires `responses`: a document without it is not one,
// and clicky's own validator says so.
const clickyOpenAPIMediaType = "application/vnd.clicky.openapi+json"

type openAPIRepresentation string

const (
	openAPIFull   openAPIRepresentation = "full"
	openAPIClicky openAPIRepresentation = "clicky"
)

// An encoded document and the tag that names those exact bytes. The tag is a
// digest of the body, so two representations can never share one — a projection
// tag cannot satisfy an If-None-Match for the complete document.
type openAPIDocument struct {
	body        []byte
	etag        string
	contentType string
}

var stubOpenAPIResponses = map[string]rpc.OpenAPIResponse{
	"200": {Description: "OK"},
}

func negotiateOpenAPIRepresentation(accept string) openAPIRepresentation {
	if strings.Contains(accept, clickyOpenAPIMediaType) {
		return openAPIClicky
	}
	return openAPIFull
}

func (rep openAPIRepresentation) contentType() string {
	if rep == openAPIClicky {
		return clickyOpenAPIMediaType
	}
	return "application/json"
}

// The operation is a map value, so replacing its responses on the copy leaves
// the document the full representation serializes untouched.
func (rep openAPIRepresentation) project(spec *rpc.OpenAPISpec) *rpc.OpenAPISpec {
	if rep != openAPIClicky {
		return spec
	}
	projected := *spec
	projected.Paths = make(map[string]rpc.OpenAPIPath, len(spec.Paths))
	for path, methods := range spec.Paths {
		reduced := make(rpc.OpenAPIPath, len(methods))
		for method, operation := range methods {
			operation.Responses = stubOpenAPIResponses
			reduced[method] = operation
		}
		projected.Paths[path] = reduced
	}
	return &projected
}

func encodeOpenAPIDocument(spec *rpc.OpenAPISpec, rep openAPIRepresentation) (openAPIDocument, error) {
	body, err := json.Marshal(rep.project(spec))
	if err != nil {
		return openAPIDocument{}, fmt.Errorf("encode OpenAPI document: %w", err)
	}
	digest := sha256.Sum256(body)
	return openAPIDocument{
		body:        body,
		etag:        fmt.Sprintf("%q", "sha256-"+hex.EncodeToString(digest[:])),
		contentType: rep.contentType(),
	}, nil
}

// The profiles are the only input to this document that changes while the
// server runs: the Cobra tree and config are complete before it serves, and the
// registered extensions are pure functions of the spec. A future extension that
// reads per-request state would break this cache — add its inputs here.
func fingerprintProfiles(profiles []query.Profile) (string, error) {
	ordered := slices.Clone(profiles)
	slices.SortFunc(ordered, func(a, b query.Profile) int {
		return strings.Compare(a.Name, b.Name)
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "", fmt.Errorf("fingerprint profiles: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// snapshotStore serves one listing for the whole generation. Both real stores
// implement Get by listing again, and a profile that imports another resolves
// through Get — so without this the document costs O(N²) reads and can be built
// from profiles that changed midway through, under a fingerprint that describes
// neither state.
type snapshotStore struct {
	profiles []query.Profile
	byName   map[string]query.Profile
}

func newSnapshotStore(profiles []query.Profile) *snapshotStore {
	byName := make(map[string]query.Profile, len(profiles))
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}
	return &snapshotStore{profiles: profiles, byName: byName}
}

func (s *snapshotStore) List(context.Context) ([]query.Profile, error) {
	return s.profiles, nil
}

func (s *snapshotStore) Get(_ context.Context, name string) (query.Profile, error) {
	return s.byName[name], nil
}

func (s *snapshotStore) Save(context.Context, query.Profile) error {
	return fmt.Errorf("profile snapshot is read-only")
}

func (s *snapshotStore) Update(context.Context, string, query.Profile, UpdateOptions) error {
	return fmt.Errorf("profile snapshot is read-only")
}

func (s *snapshotStore) Delete(context.Context, string) error {
	return fmt.Errorf("profile snapshot is read-only")
}

// The generator carries one components map across generations, so a filter
// registered for a profile outlives the profile — the paths and surfaces are
// rebuilt from the store on every generation, and these were not.
func dropProfileFilterComponents(spec *rpc.OpenAPISpec) {
	if spec.Components == nil || spec.Components.ClickyFilters == nil {
		return
	}
	for name := range spec.Components.ClickyFilters {
		if !strings.HasPrefix(name, "profile-") {
			continue
		}
		if strings.Contains(name, "-column-") || strings.Contains(name, "-param-") {
			delete(spec.Components.ClickyFilters, name)
		}
	}
}
