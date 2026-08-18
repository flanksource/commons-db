package profiles

import (
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type cursorOnlyProvider struct {
	pages        []query.PageRequest
	executeCalls int
	// windows is the resolved value of the profile's time-from param on each
	// page, so a walk can assert it resolved its date math once rather than
	// once per request.
	windows []string
}

func (*cursorOnlyProvider) Type() string { return "cursor-only-profile-test" }

func (*cursorOnlyProvider) PagingModes() query.PagingMode { return query.PagingCursor }

func (p *cursorOnlyProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	p.executeCalls++
	return nil, fmt.Errorf("cursor-only provider must not execute the full query")
}

func (p *cursorOnlyProvider) Pages(
	_ dbcontext.Context,
	req query.ProviderRequest,
	request query.PageRequest,
) iter.Seq2[query.Page, error] {
	p.pages = append(p.pages, request)
	if start, ok := req.Params["Start"]; ok {
		p.windows = append(p.windows, fmt.Sprint(start))
	}
	rowID := "row-1"
	hasMore := true
	if len(req.Position.Keys) > 0 {
		if len(req.Position.Keys) != 1 || fmt.Sprint(req.Position.Keys[0]) != "row-1" {
			return query.ErrorPage(fmt.Errorf("unexpected cursor position: %v", req.Position.Keys))
		}
		rowID, hasMore = "row-2", false
	}
	return func(yield func(query.Page, error) bool) {
		page := query.Page{Rows: []query.Row{{"id": rowID}}, HasMore: hasMore}
		if hasMore {
			page.NextKeys = []any{rowID}
		}
		yield(page, nil)
	}
}

func newCursorOnlyHandler(profile query.Profile, provider *cursorOnlyProvider) http.Handler {
	GinkgoHelper()
	query.RegisterProvider(provider)
	store, err := NewFileStore(GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())
	Expect(store.Save(GinkgoT().Context(), profile)).To(Succeed())
	return newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
}

var _ = Describe("cursor-only profile paging", func() {
	profile := query.Profile{
		Name: "Cursor only", Provider: query.ProviderConfig{Type: "cursor-only-profile-test"}, Query: "rows",
		Order: query.Order{{Column: "id", Unique: true}},
	}

	It("uses a cursor for the initial HTTP page without executing the full query", func() {
		provider := &cursorOnlyProvider{}
		handler := newCursorOnlyHandler(profile, provider)

		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/cursor-only?format=json&limit=1", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(provider.executeCalls).To(BeZero())
		Expect(provider.pages).To(HaveLen(1))
		Expect(provider.pages[0].Mode()).To(Equal(query.PagingCursor))
		Expect(response.Header().Get("X-Page-Offset")).To(BeEmpty())
		Expect(response.Header().Get("X-Next-Cursor")).ToNot(BeEmpty())
	})

	It("reads a second HTTP page from the cursor returned by the first", func() {
		provider := &cursorOnlyProvider{}
		handler := newCursorOnlyHandler(profile, provider)

		first := httptest.NewRecorder()
		handler.ServeHTTP(first, httptest.NewRequest(
			http.MethodGet, "/api/v1/profile/cursor-only?format=json&limit=1", nil,
		))
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		cursor := first.Header().Get("X-Next-Cursor")
		Expect(cursor).ToNot(BeEmpty())

		second := httptest.NewRecorder()
		handler.ServeHTTP(second, httptest.NewRequest(
			http.MethodGet,
			"/api/v1/profile/cursor-only?format=json&limit=1&cursor="+url.QueryEscape(cursor),
			nil,
		))
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())

		var firstRows, secondRows []query.Row
		Expect(json.Unmarshal(first.Body.Bytes(), &firstRows)).To(Succeed())
		Expect(json.Unmarshal(second.Body.Bytes(), &secondRows)).To(Succeed())
		Expect(firstRows).To(Equal([]query.Row{{"id": "row-1"}}))
		Expect(secondRows).To(Equal([]query.Row{{"id": "row-2"}}))
		Expect(provider.pages).To(HaveLen(2))
		Expect(provider.pages[1].Mode()).To(Equal(query.PagingCursor))
	})

	// A profile whose window is a time-from/time-to parameter pair resolves its
	// date math per request, and the resolved value is fingerprinted into the
	// cursor. Without a pinned clock "now-2d" landed on a different nanosecond on
	// every page, so the second request could never match the first and every
	// walk died on page two with cursor_stale.
	It("resumes a walk whose window is written as date math", func() {
		rolling := profile
		rolling.Name = "Cursor rolling"
		rolling.Params = []query.ParamDef{
			{Name: "Start", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeFrom},
			{Name: "End", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeTo},
		}
		provider := &cursorOnlyProvider{}
		handler := newCursorOnlyHandler(rolling, provider)
		window := "&Start=now-2d&End=now"

		first := httptest.NewRecorder()
		handler.ServeHTTP(first, httptest.NewRequest(
			http.MethodGet, "/api/v1/profile/cursor-rolling?format=json&limit=1"+window, nil,
		))
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		cursor := first.Header().Get("X-Next-Cursor")
		Expect(cursor).ToNot(BeEmpty())

		second := httptest.NewRecorder()
		handler.ServeHTTP(second, httptest.NewRequest(
			http.MethodGet,
			"/api/v1/profile/cursor-rolling?format=json&limit=1"+window+"&cursor="+url.QueryEscape(cursor),
			nil,
		))
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())

		var secondRows []query.Row
		Expect(json.Unmarshal(second.Body.Bytes(), &secondRows)).To(Succeed())
		Expect(secondRows).To(Equal([]query.Row{{"id": "row-2"}}))
		// One walk, one window: the second page read the clock off the cursor
		// rather than taking its own.
		Expect(provider.windows).To(HaveLen(2))
		Expect(provider.windows[1]).To(Equal(provider.windows[0]))
	})

	It("stales the cursor when the window itself changes", func() {
		rolling := profile
		rolling.Name = "Cursor rewound"
		rolling.Params = []query.ParamDef{
			{Name: "Start", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeFrom},
		}
		handler := newCursorOnlyHandler(rolling, &cursorOnlyProvider{})

		first := httptest.NewRecorder()
		handler.ServeHTTP(first, httptest.NewRequest(
			http.MethodGet, "/api/v1/profile/cursor-rewound?format=json&limit=1&Start=now-2d", nil,
		))
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		cursor := first.Header().Get("X-Next-Cursor")

		second := httptest.NewRecorder()
		handler.ServeHTTP(second, httptest.NewRequest(
			http.MethodGet,
			"/api/v1/profile/cursor-rewound?format=json&limit=1&Start=now-4h&cursor="+url.QueryEscape(cursor),
			nil,
		))
		Expect(second.Code).To(Equal(http.StatusBadRequest), second.Body.String())
		Expect(second.Body.String()).To(ContainSubstring("cursor_stale"))
	})

	It("advertises a cursor but no offset", func() {
		query.RegisterProvider(&cursorOnlyProvider{})
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
		Expect(addProfileToSpec(spec, profile)).To(Succeed())

		roles := map[string]bool{}
		for _, parameter := range spec.Paths["/api/v1/profile/profile-cursor-only"]["get"].Parameters {
			if parameter.Clicky != nil {
				roles[string(parameter.Clicky.Role)] = true
			}
		}
		Expect(roles["cursor"]).To(BeTrue())
		Expect(roles["offset"]).To(BeFalse())
	})
})
