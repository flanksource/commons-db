package providers_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A profile's own size caps every request below the page a caller asks for.
// The cap bounds one request, not the data: a page cut to it is followed by the
// rest of the index, a cap's-size page at a time.
var _ = Describe("OpenSearch paging under a profile size cap", func() {
	const (
		profileCap = 2
		pageLimit  = 5
	)
	ids := []string{"a", "b", "c", "d", "e"}

	// served answers request n with the n-th cap-sized slice of ids.
	served := func(call int) string {
		start := min(call*profileCap, len(ids))
		end := min(start+profileCap, len(ids))
		hits := make([]string, 0, end-start)
		for i, id := range ids[start:end] {
			sort, err := json.Marshal([]any{start + i, id})
			Expect(err).ToNot(HaveOccurred())
			hits = append(hits, fmt.Sprintf(`{"_index":"logs","_id":%q,"_source":{"message":%q},"sort":%s}`, id, id, sort))
		}
		return fmt.Sprintf(`{"took":1,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`,
			len(ids), strings.Join(hits, ","))
	}
	order := query.Order{{Column: "@timestamp"}, {Column: "_id", Unique: true}}
	capped := func(stub *openSearchStub) query.Profile {
		profile := stub.profile("capped", order)
		profile.Provider.Options["limit"] = fmt.Sprint(profileCap)
		return profile
	}
	type walked struct {
		Rows      int
		HasMore   bool
		Truncated bool
	}
	walk := func(profile query.Profile, request query.PageRequest) ([]walked, []string) {
		var pages []walked
		var messages []string
		for page, err := range query.ExecutePages(context.New(), profile, request) {
			Expect(err).ToNot(HaveOccurred())
			pages = append(pages, walked{Rows: len(page.Rows), HasMore: page.HasMore, Truncated: page.Truncated})
			for _, row := range page.Rows {
				messages = append(messages, fmt.Sprint(row["message"]))
			}
		}
		return pages, messages
	}

	It("keeps offset paging past a page the cap held short, stepping by the rows it read", func() {
		stub := newOpenSearchStub(served)
		defer stub.server.Close()

		pages, messages := walk(capped(stub), query.PageRequest{Limit: pageLimit})
		Expect(pages).To(Equal([]walked{
			{Rows: 2, HasMore: true, Truncated: true},
			{Rows: 2, HasMore: true, Truncated: true},
			{Rows: 1, HasMore: false, Truncated: true},
		}))
		Expect(messages).To(Equal(ids))
		Expect(stub.sizes).To(Equal([]string{"2", "2", "2"}))
		Expect([]any{stub.bodies[0]["from"], stub.bodies[1]["from"], stub.bodies[2]["from"]}).
			To(Equal([]any{nil, float64(2), float64(4)}))
	})

	It("keeps a scroll walk going past a page the cap held short", func() {
		stub := newOpenSearchStub(served)
		defer stub.server.Close()
		profile := capped(stub)
		profile.Provider.Options["pagingThreshold"] = 0

		pages, messages := walk(profile, query.PageRequest{Limit: pageLimit, Strategy: query.PagingCursor})
		Expect(pages).To(HaveLen(3))
		Expect([]bool{pages[0].HasMore, pages[1].HasMore, pages[2].HasMore}).To(Equal([]bool{true, true, false}))
		Expect(messages).To(Equal(ids))
		Expect(stub.openedScrolls).To(Equal(1))
		Expect(stub.continuedScrolls).To(Equal(2))
		Expect(stub.clearedScrolls).To(Equal(1))
	})
})
