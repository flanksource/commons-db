// Read hooks: the contract a data source implements to prepare profile reads,
// and the errors it wraps so the transport answers with the right status.
package profilestore

import (
	"context"
	"errors"
	"sync"

	"github.com/flanksource/commons-db/query"
)

// ReadRequest is one resolved profile read and the parameters the caller
// supplied, unresolved. Params is the map the caller executes the read with,
// and a hook may rewrite it in place: a data source that indexes what a
// caller names under another id — a routed record stream — binds that id, which
// only the hook, running under the request's context, can resolve.
type ReadRequest struct {
	Profile query.Profile
	Params  map[string]any
}

// BeforeExecuteFunc prepares one or more profile reads and returns the release
// that keeps their data readable. The caller holds release until every read in
// the batch has finished. Its error fails the whole batch; nothing is read
// after it.
//
// It is the seam a data source that has to be prepared per request plugs into
// — a record stream index caught up to its source before the index is paged.
// A hook may rewrite a read's Params (see ReadRequest), and must leave a map
// it already rewrote as it is when it prepares that map again.
type BeforeExecuteFunc func(ctx context.Context, reads []ReadRequest) (release func(), err error)

var (
	// ErrProfileDataNotFound is what a BeforeExecuteFunc wraps when the data a
	// request names does not exist, so the transport answers 404 rather than
	// an empty result that reads as "nothing matched".
	ErrProfileDataNotFound = errors.New("profile data not found")

	// ErrProfileRequestInvalid is what a BeforeExecuteFunc wraps when the
	// request itself cannot be served — a param missing or malformed — so the
	// transport answers 400. Every failure it does not wrap is the server's.
	ErrProfileRequestInvalid = errors.New("profile request invalid")

	// ErrProfileForbidden is what a BeforeExecuteFunc wraps when the request
	// names data the caller may not read, so the transport answers 403.
	ErrProfileForbidden = errors.New("profile data forbidden")
)

// PrepareReads runs hook and normalises its cleanup contract. A hook that
// acquired resources before failing still gets its release called; a successful
// caller always gets a non-nil, exactly-once cleanup.
func PrepareReads(ctx context.Context, hook BeforeExecuteFunc, reads []ReadRequest) (func(), error) {
	if hook == nil {
		return func() {}, nil
	}
	release, err := hook(ctx, reads)
	if release == nil {
		release = func() {}
	}
	var once sync.Once
	releaseOnce := func() { once.Do(release) }
	if err != nil {
		releaseOnce()
		return func() {}, err
	}
	return releaseOnce, nil
}
