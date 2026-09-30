package profiles

import (
	"context"
	"errors"
	"net/http"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

func (s *Service) prepareReads(ctx context.Context, reads []profilestore.ReadRequest) (func(), error) {
	return profilestore.PrepareReads(ctx, s.beforeExecute, reads)
}

func (s *Service) prepareRead(ctx context.Context, p query.Profile, params map[string]any) (func(), error) {
	return s.prepareReads(ctx, []profilestore.ReadRequest{{Profile: p, Params: params}})
}

// PrepareErrorStatus is the HTTP status and code a failed BeforeExecuteFunc
// answers with. Only a cause the hook named is the caller's; anything else —
// a store that is down, an index that could not be written — is a 500, since
// a 4xx would send the caller to change a request that was fine.
func PrepareErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, profilestore.ErrProfileDataNotFound):
		return http.StatusNotFound, "profile_data_not_found"
	case errors.Is(err, dbcontext.ErrConnectionExpired):
		return http.StatusGone, "profile_data_expired"
	case errors.Is(err, profilestore.ErrProfileRequestInvalid):
		return http.StatusBadRequest, "invalid_params"
	case errors.Is(err, profilestore.ErrProfileForbidden):
		return http.StatusForbidden, "profile_forbidden"
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
