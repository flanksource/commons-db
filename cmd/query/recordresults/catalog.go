package recordresults

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

var errReadOnly = errors.New("record result profiles are read-only")

// List returns every result profile by name.
func (r *Registry) List(context.Context) ([]query.Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]query.Profile, 0, len(r.results))
	for _, result := range r.results {
		items = append(items, result.profile)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// Get returns the result profile named name.
func (r *Registry) Get(_ context.Context, name string) (query.Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result, ok := r.results[name]
	if !ok {
		return query.Profile{}, fmt.Errorf("record result profile %q not found", name)
	}
	return result.profile, nil
}

// Peek is Get: reading a result profile has no expiry to slide.
func (r *Registry) Peek(ctx context.Context, name string) (query.Profile, error) {
	return r.Get(ctx, name)
}

// IsVirtual reports whether name is a result profile.
func (r *Registry) IsVirtual(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.results[name]
	return ok
}

func (r *Registry) Save(context.Context, query.Profile) error { return errReadOnly }

func (r *Registry) Update(context.Context, string, query.Profile, profiles.UpdateOptions) error {
	return errReadOnly
}

func (r *Registry) Delete(context.Context, string) error { return errReadOnly }

// owns answers only to the index connection's id and to its reference in the
// registry's namespace. A bare name, or the name outside that namespace, may
// be a connection someone else configured, and resolving it here would read
// that connection's queries against the record index.
func (r *Registry) owns(reference string) bool {
	return reference == r.connection.ID.String() || reference == r.connectionReference()
}

// ResolveConnection is the dbcontext.ConnectionResolver for the index
// connection. Any other reference is not the registry's to answer, so it
// reports none and resolution carries on.
func (r *Registry) ResolveConnection(reference string) (*models.Connection, error) {
	if !r.owns(reference) {
		return nil, nil
	}
	connection := r.connection
	return &connection, nil
}

// BeforeExecute is the profiles.BeforeExecuteFunc for result profiles: it
// catches every requested stream up, then holds one lease across the whole
// read batch. Revalidation under that lease closes the gap in which a sweep can
// remove an index stream after Ensure returns but before its query starts.
// A stream that does not exist, or that holds another result type, is
// profiles.ErrProfileDataNotFound — an empty page would say "nothing matched"
// about a stream nobody wrote. Every other profile passes through untouched.
func (r *Registry) BeforeExecute(ctx context.Context, reads []profiles.ReadRequest) (func(), error) {
	targets, err := r.readTargets(reads)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return func() {}, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := r.ensureTargets(ctx, targets); err != nil {
			return nil, err
		}
		release := r.index.Lease()
		err := r.validateTargets(ctx, targets)
		if err == nil {
			return release, nil
		}
		release()
		if !errors.Is(err, recordstore.ErrNotFound) || attempt == 1 {
			return nil, notFound(err)
		}
	}
	panic("unreachable")
}

type readTarget struct {
	stream string
	kind   string
}

func (r *Registry) readTargets(reads []profiles.ReadRequest) ([]readTarget, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	targets := make([]readTarget, 0, len(reads))
	seen := make(map[string]readTarget, len(reads))
	for _, read := range reads {
		result, ok := r.results[read.Profile.Name]
		if !ok {
			continue
		}
		stream, err := requestedStream(read.Profile.Name, read.Params)
		if err != nil {
			return nil, err
		}
		for _, name := range result.baseOnly {
			if _, named := read.Params[name]; named {
				return nil, fmt.Errorf("%w: profile %q is a view of %q results and takes no %s param; read %q for it",
					profiles.ErrProfileRequestInvalid, read.Profile.Name, result.Kind, name, result.Profile)
			}
		}
		target := readTarget{stream: stream, kind: result.Kind}
		if previous, ok := seen[stream]; ok {
			if previous.kind != target.kind {
				return nil, fmt.Errorf("%w: stream %q is requested as both %q and %q results",
					profiles.ErrProfileDataNotFound, stream, previous.kind, target.kind)
			}
			continue
		}
		seen[stream] = target
		targets = append(targets, target)
	}
	return targets, nil
}

func (r *Registry) ensureTargets(ctx context.Context, targets []readTarget) error {
	for _, target := range targets {
		if err := r.indexer.Ensure(ctx, target.stream); err != nil {
			return notFound(err)
		}
	}
	return nil
}

func (r *Registry) validateTargets(ctx context.Context, targets []readTarget) error {
	for _, target := range targets {
		meta, err := r.index.Meta(ctx, target.stream)
		if err != nil {
			return err
		}
		if meta.Kind != target.kind {
			return fmt.Errorf("%w: stream %q holds %q results, not %q",
				profiles.ErrProfileDataNotFound, target.stream, meta.Kind, target.kind)
		}
	}
	return nil
}

// requestedStream is the stream a request names. A missing or malformed id is
// the caller's to fix, so it is profiles.ErrProfileRequestInvalid.
func requestedStream(profile string, params map[string]any) (string, error) {
	value, ok := params[streamParam]
	stream, isString := value.(string)
	if !ok || !isString || stream == "" {
		return "", fmt.Errorf("%w: profile %q reads one record stream; pass its id as the %s param",
			profiles.ErrProfileRequestInvalid, profile, streamParam)
	}
	if err := recordstore.ValidateStream(stream); err != nil {
		return "", fmt.Errorf("%w: %w", profiles.ErrProfileRequestInvalid, err)
	}
	return stream, nil
}

func notFound(err error) error {
	if errors.Is(err, recordstore.ErrNotFound) {
		return fmt.Errorf("%w: %w", profiles.ErrProfileDataNotFound, err)
	}
	return err
}
