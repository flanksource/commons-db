package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/flanksource/clicky/entity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// failingListStore is a store whose listing fails the way a store behind an
// unreachable database does.
type failingListStore struct{ Store }

func (failingListStore) List(context.Context) ([]query.Profile, error) {
	return nil, errors.New("profile store unavailable")
}

func surfaceService(store Store) *Service {
	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	Expect(err).ToNot(HaveOccurred())
	return service
}

func execErrorOf(handler http.Handler, path string) (int, execError) {
	response := get(handler, path, "application/json")
	var body execError
	Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed(), response.Body.String())
	return response.Code, body
}

var _ = Describe("resolving a profile surface key", func() {
	var base *FileStore

	BeforeEach(func() {
		query.RegisterProvider(familyLookupMock{})
		var err error
		base, err = NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
	})

	Context("when the store cannot be listed", func() {
		It("answers the family with a server error, not an unknown profile", func() {
			_, err := surfaceService(failingListStore{Store: base}).resolveSurface(context.Background(), "profile-results-spans")
			Expect(err).To(MatchError(ContainSubstring("profile store unavailable")))
			var status *entity.StatusError
			Expect(errors.As(err, &status)).To(BeFalse(), "a store failure was reported as %v", status)
		})

		It("answers an execution with a 500", func() {
			handler, err := surfaceService(failingListStore{Store: base}).Handler("/api/v1", &nextMarker{})
			Expect(err).ToNot(HaveOccurred())
			code, body := execErrorOf(handler, "/api/v1/profile/profile-results-spans")
			Expect(code).To(Equal(http.StatusInternalServerError))
			Expect(body.Code).To(Equal("profile_store_failed"))
		})
	})

	Context("when a saved profile and a virtual one share a surface key", func() {
		var service *Service

		BeforeEach(func() {
			Expect(base.Save(context.Background(), lookupProfile("results-spans"))).To(Succeed())
			overlay, err := NewOverlayStore(base, nameOnlyVirtualStore{profile: lookupProfile("results/spans")})
			Expect(err).ToNot(HaveOccurred())
			service = surfaceService(overlay)
		})

		It("refuses to pick one when resolving the family", func() {
			_, err := service.resolveSurface(context.Background(), "profile-results-spans")
			var status *entity.StatusError
			Expect(errors.As(err, &status)).To(BeTrue(), "%v", err)
			Expect(status.StatusCode()).To(Equal(http.StatusInternalServerError))
			Expect(status.Code).To(Equal("profile_surface_conflict"))
			Expect(status.Message).To(And(ContainSubstring(`"results-spans"`), ContainSubstring(`"results/spans"`)))
		})

		It("refuses to pick one when executing", func() {
			handler, err := service.Handler("/api/v1", &nextMarker{})
			Expect(err).ToNot(HaveOccurred())
			code, body := execErrorOf(handler, "/api/v1/profile/profile-results-spans")
			Expect(code).To(Equal(http.StatusInternalServerError))
			Expect(body.Code).To(Equal("profile_surface_conflict"))
		})

		It("refuses to describe both in the surface listing", func() {
			_, err := service.listSurfaces(context.Background())
			Expect(err).To(MatchError(ContainSubstring(`surface "profile-results-spans"`)))
		})
	})

	It("refuses to save a profile whose surface key a virtual profile already has", func() {
		overlay, err := NewOverlayStore(base, nameOnlyVirtualStore{profile: lookupProfile("results/spans")})
		Expect(err).ToNot(HaveOccurred())
		Expect(overlay.Save(context.Background(), lookupProfile("results-spans"))).
			To(MatchError(ContainSubstring(`surface "profile-results-spans" of virtual profile "results/spans"`)))
		Expect(overlay.Update(context.Background(), "other", lookupProfile("results-spans"), UpdateOptions{})).
			To(MatchError(ContainSubstring(`surface "profile-results-spans" of virtual profile "results/spans"`)))
	})
})
