package sessions

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

// sessionRefProvider answers `ref:<name>` by reading profiles.<name> the way
// metric SQL does (query.ProfileTables), and any other query with one row
// naming it.
type sessionRefProvider struct{}

func (sessionRefProvider) Type() string { return "session-profile-ref-mock" }

func (sessionRefProvider) Execute(ctx dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	name, ok := strings.CutPrefix(req.Query, "ref:")
	if !ok {
		return []query.Row{{"from": req.Query, "namespace": req.Params["namespace"]}}, nil
	}
	table, err := query.ProfileTables(ctx, req.Params)(name)
	return table.Rows, err
}

var _ = ginkgo.Describe("profiles.<name> in a top session", func() {
	const namespace = "acme"
	var (
		store profilestore.Store
		mu    sync.Mutex
		reads []string
	)
	refProfile := func(name, q string) query.Profile {
		return query.Profile{
			Name: name, Query: q, Provider: query.ProviderConfig{Type: "session-profile-ref-mock"},
			Params: []query.ParamDef{{Name: "namespace"}},
		}
	}
	hook := func(_ context.Context, batch []profilestore.ReadRequest) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		for _, read := range batch {
			reads = append(reads, read.Profile.Name)
		}
		return func() {}, nil
	}
	prepared := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reads...)
	}
	wantRows := []query.Row{{"from": "leaf", "namespace": namespace}}

	ginkgo.BeforeEach(func() {
		reads = nil
		query.RegisterProvider(sessionRefProvider{})
		fileStore, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		for _, p := range []query.Profile{refProfile("parent", "ref:child"), refProfile("child", "leaf")} {
			Expect(fileStore.Save(context.Background(), p)).To(Succeed())
		}
		store = fileStore
	})

	latestRows := func(session *query.Session) func() []query.Row {
		return func() []query.Row {
			if latest := session.Latest(); latest != nil {
				return latest.Rows
			}
			return nil
		}
	}

	ginkgo.It("reads the child of a session started over HTTP, preparing it with the registry's BeforeRead", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: func(
			ctx context.Context, p query.Profile, params map[string]any,
		) (func(), error) {
			return profilestore.PrepareReads(ctx, hook, []profilestore.ReadRequest{{Profile: p, Params: params}})
		}})
		ginkgo.DeferCleanup(registry.StopAll)
		handler := newSessionHandler(sessionHandlerOptions{
			Prefix: "/api/v1", Ctx: dbcontext.New(), Store: store, Registry: registry, Next: &nextMarker{},
		})

		response := doReq(handler, http.MethodPost, "/api/v1/profile/parent/sessions?interval=1s&namespace="+namespace)
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		session, ok := registry.Get(decodeInfo(response).ID)
		Expect(ok).To(BeTrue())

		Eventually(latestRows(session)).Should(Equal(wantRows))
		Expect(prepared()[:2]).To(Equal([]string{"parent", "child"}), "a later sample may prepare again")
	})

	ginkgo.It("reads the child of a session started from the CLI, preparing it with BeforeExecute", func() {
		var stdout, stderr bytes.Buffer
		runner, err := NewRunner(RunnerOptions{
			Profiles: func() (profilestore.Store, error) { return store, nil },
			Context:  func() dbcontext.Context { return dbcontext.New() },
			Stdout:   &stdout, Stderr: &stderr, BeforeExecute: hook,
		})
		Expect(err).ToNot(HaveOccurred())
		parent := refProfile("parent", "ref:child")
		parent.Top = &query.TopSpec{}

		session, err := runner.startCLISession(store, parent, map[string]any{"namespace": namespace})
		Expect(err).ToNot(HaveOccurred())
		ginkgo.DeferCleanup(func() { session.Stop("spec done") })

		Eventually(latestRows(session)).Should(Equal(wantRows))
		Expect(prepared()[:2]).To(Equal([]string{"parent", "child"}), "a later sample may prepare again")
	})
})
