package profiles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/clicky/entity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// stubSnapshots stands in for the snapshot manager so the status mapping can be
// exercised without a reconciliation behind it.
type stubSnapshots struct {
	descriptor ReconcileSnapshotDescriptor
	err        error
}

func (s stubSnapshots) Create(context.Context, *query.ReconcileResult, time.Duration) (ReconcileSnapshotDescriptor, error) {
	return ReconcileSnapshotDescriptor{}, nil
}

func (s stubSnapshots) Materialize(context.Context, ReconcileMaterializeOptions) (ReconcileSnapshotDescriptor, error) {
	return ReconcileSnapshotDescriptor{}, nil
}

func (s stubSnapshots) Describe(context.Context, string) (ReconcileSnapshotDescriptor, error) {
	return s.descriptor, s.err
}

// statusOf reports the HTTP status an error carries, which is what the browser
// reads to tell an aged-out run from a bad link.
func statusOf(err error) (int, string) {
	var status *entity.StatusError
	if !errors.As(err, &status) {
		return 0, ""
	}
	return status.StatusCode(), status.Code
}

var _ = Describe("reading a reconciliation snapshot", func() {
	const id = "3f1c8a24-9f2b-4c31-8f0e-2a7b6d5c4e13"

	snapshotService := func(snapshots SnapshotService) *Service {
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		built, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			Snapshots:  snapshots,
		})
		Expect(err).ToNot(HaveOccurred())
		return built
	}

	It("returns the descriptor of a snapshot its profile owns", func() {
		descriptor := ReconcileSnapshotDescriptor{ID: id, Source: "orders-emitted"}
		result, err := snapshotService(stubSnapshots{descriptor: descriptor}).GetReconcileSnapshot(
			context.Background(), "orders-emitted", ReconcileSnapshotFlags{SnapshotID: id})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.ID).To(Equal(id))
	})

	// Aging out is a lifecycle event, not a fault, and the browser says
	// something different about it than about a link that was never valid.
	It("answers 410 for a snapshot that expired", func() {
		_, err := snapshotService(stubSnapshots{err: dbcontext.ErrConnectionExpired}).GetReconcileSnapshot(
			context.Background(), "orders-emitted", ReconcileSnapshotFlags{SnapshotID: id})

		status, code := statusOf(err)
		Expect(status).To(Equal(http.StatusGone))
		Expect(code).To(Equal("snapshot_expired"))
	})

	It("answers 404 for an id it has never seen", func() {
		_, err := snapshotService(stubSnapshots{
			err: fmt.Errorf("snapshot %q: %w", id, ErrSnapshotNotFound),
		}).GetReconcileSnapshot(
			context.Background(), "orders-emitted", ReconcileSnapshotFlags{SnapshotID: id})

		status, code := statusOf(err)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(code).To(Equal("snapshot_not_found"))
	})

	// The {id} segment is the source profile; without this check the route would
	// serve any profile's snapshot from any profile's URL.
	It("refuses a snapshot that belongs to another profile", func() {
		descriptor := ReconcileSnapshotDescriptor{ID: id, Source: "somebody-elses-profile"}
		_, err := snapshotService(stubSnapshots{descriptor: descriptor}).GetReconcileSnapshot(
			context.Background(), "orders-emitted", ReconcileSnapshotFlags{SnapshotID: id})

		status, code := statusOf(err)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(code).To(Equal("snapshot_not_found"))
	})

	It("rejects a request that names no snapshot", func() {
		_, err := snapshotService(stubSnapshots{}).GetReconcileSnapshot(
			context.Background(), "orders-emitted", ReconcileSnapshotFlags{})

		status, code := statusOf(err)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(code).To(Equal("invalid_request"))
	})
})
