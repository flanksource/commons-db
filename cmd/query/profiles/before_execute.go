package profiles

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// ReadRequest is one resolved profile read and the parameters the caller
// supplied, unresolved.
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

func (s *Service) prepareReads(ctx context.Context, reads []ReadRequest) (func(), error) {
	return PrepareReads(ctx, s.beforeExecute, reads)
}

func (s *Service) prepareRead(ctx context.Context, p query.Profile, params map[string]any) (func(), error) {
	return s.prepareReads(ctx, []ReadRequest{{Profile: p, Params: params}})
}

// PrepareErrorStatus is the HTTP status and code a failed BeforeExecuteFunc
// answers with. Only a cause the hook named is the caller's; anything else —
// a store that is down, an index that could not be written — is a 500, since
// a 4xx would send the caller to change a request that was fine.
func PrepareErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrProfileDataNotFound):
		return http.StatusNotFound, "profile_data_not_found"
	case errors.Is(err, dbcontext.ErrConnectionExpired):
		return http.StatusGone, "profile_data_expired"
	case errors.Is(err, ErrProfileRequestInvalid):
		return http.StatusBadRequest, "invalid_params"
	default:
		return http.StatusInternalServerError, "prepare_failed"
	}
}

// writePrepareError answers a failed hook with the status its cause names.
func writePrepareError(w http.ResponseWriter, err error) {
	status, code := PrepareErrorStatus(err)
	writeExecError(w, status, code, err)
}

// prepareStatusError is a failed hook for a read clicky's transport answers — a
// filter lookup — carrying the status and code writePrepareError would. An
// unclassified error there is an opaque 500, which would tell a caller whose
// stream expired that the server broke.
func prepareStatusError(err error) *entity.StatusError {
	status, code := PrepareErrorStatus(err)
	return entity.NewStatusError(status, code, err.Error())
}
