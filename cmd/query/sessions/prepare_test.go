package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

var _ = ginkgo.Describe("a session over a profile whose data is prepared per read", func() {
	var (
		store   profilepkg.Store
		hookErr error
		hooked  chan string
	)

	hook := func(_ context.Context, reads []profilepkg.ReadRequest) (func(), error) {
		hooked <- fmt.Sprintf("%s region=%v", reads[0].Profile.Name, reads[0].Params["region"])
		return func() { hooked <- "release" }, hookErr
	}

	ginkgo.BeforeEach(func() {
		hookErr, hooked = nil, make(chan string, 10)
		query.RegisterProvider(&execMock{rows: []query.Row{{"id": 1}}})
		fileStore, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(fileStore.Save(context.Background(), execProfile("plain-top"))).To(Succeed())
		store = fileStore
	})

	start := func() (int, string) {
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: func(
			ctx context.Context, p query.Profile, params map[string]any,
		) (func(), error) {
			return profilepkg.PrepareReads(ctx, hook, []profilepkg.ReadRequest{{Profile: p, Params: params}})
		}})
		ginkgo.DeferCleanup(registry.StopAll)
		handler := newSessionHandler(sessionHandlerOptions{
			Prefix: "/api/v1", Ctx: dbcontext.New(), Store: store, Registry: registry, Next: &nextMarker{},
		})
		response := doReq(handler, http.MethodPost, "/api/v1/profile/plain-top/sessions?interval=1s&region=EU")
		return response.Code, response.Body.String()
	}

	ginkgo.It("prepares the data with the request's params before the session starts", func() {
		code, body := start()
		Expect(code).To(Equal(http.StatusCreated), body)
		Eventually(hooked).Should(Receive(Equal("plain-top region=EU")))
		Eventually(hooked).Should(Receive(Equal("release")))
	})

	ginkgo.DescribeTable("answers a start it could not prepare by the hook's cause",
		func(cause error, status int) {
			hookErr = cause
			code, body := start()
			Expect(code).To(Equal(status), body)
			Expect(body).To(ContainSubstring("run-9"))
			Expect(hooked).To(Receive(Equal("plain-top region=EU")))
			Expect(hooked).To(Receive(Equal("release")))
		},
		ginkgo.Entry("data that does not exist", fmt.Errorf("stream run-9: %w", profilepkg.ErrProfileDataNotFound), http.StatusNotFound),
		ginkgo.Entry("a request the hook refused", fmt.Errorf("stream run-9: %w", profilepkg.ErrProfileRequestInvalid), http.StatusBadRequest),
		ginkgo.Entry("a store that is down", errors.New("stream run-9: connection refused"), http.StatusInternalServerError),
	)

	ginkgo.It("prepares the data of a session started from the CLI", func() {
		hookErr = errors.New("index unavailable")
		var stdout, stderr bytes.Buffer
		runner, err := NewRunner(RunnerOptions{
			Profiles: func() (profilepkg.Store, error) { return store, nil },
			Context:  func() dbcontext.Context { return dbcontext.New() },
			Stdout:   &stdout, Stderr: &stderr, BeforeExecute: hook,
		})
		Expect(err).ToNot(HaveOccurred())
		profile := execProfile("plain-top")
		profile.Top = &query.TopSpec{}

		_, err = runner.startCLISession(profile, map[string]any{"region": "US"})
		Expect(errors.Is(err, query.ErrPrepareRead)).To(BeTrue(), "%v", err)
		Expect(hooked).To(Receive(Equal("plain-top region=US")))
		Expect(hooked).To(Receive(Equal("release")))
	})
})
