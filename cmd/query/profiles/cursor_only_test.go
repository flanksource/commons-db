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
