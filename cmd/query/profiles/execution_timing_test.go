package profiles

import (
	"net/http"
	"regexp"
	"strconv"
	"testing"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

// The execution runs under the server's context, not the request's, so the
// request's Server-Timing accumulator has to be carried across for the engine's
// phases to reach the response header the listing UI reads.
func TestExecHandlerReportsEnginePhasesInServerTiming(t *testing.T) {
	h, _, mock := newExecTest(t, execProfile("activities"))

	rec := get(rpchttp.TimingMiddleware(h), "/api/v1/profile/activities?region=EU", "application/json+clicky")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	want := regexp.MustCompile(`^total;dur=[0-9.]+, provider;dur=[0-9.]+;desc="rows=` +
		strconv.Itoa(len(mock.rows)) + `", transform;dur=[0-9.]+$`)
	if header := rec.Header().Get("Server-Timing"); !want.MatchString(header) {
		t.Fatalf("Server-Timing = %q, want it to match %s", header, want)
	}
}
