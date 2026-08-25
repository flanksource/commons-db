package providers_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// The tail is measured against a real cluster because the two things it can get
// wrong are both invisible to a stub.
//
// A stub answers the instant it is asked, so it cannot reproduce
// index.refresh_interval — the window where a document has been accepted and is
// not yet searchable, which is exactly where a poll loop either waits correctly
// or skips a line. And a stub implements search_after the way the test author
// believes it works, so a walk that resumed in the wrong place would agree with
// its own stub and disagree only with OpenSearch.
//
// Set COMMONS_DB_OPENSEARCH_URL to run these. Credentials travel in the URL:
// http://admin:secret@localhost:9200
var _ = Describe("opensearch tail (live)", Ordered, func() {
	const index = "commons-db-tail-it"

	var address string

	index_ := func(id string, at time.Time, message string) {
		GinkgoHelper()
		Expect(openSearchAdmin(address, http.MethodPost, "/"+index+"/_doc/"+id+"?refresh=true", fmt.Sprintf(
			`{"@timestamp":%q,"message":%q}`, at.UTC().Format(time.RFC3339Nano), message))).To(Succeed())
	}

	request := func(extra map[string]any) query.ProviderRequest {
		options := map[string]any{
			"address":  address,
			"index":    index,
			"tailPoll": "200ms",
			"search":   map[string]any{"timeField": "@timestamp"},
		}
		for key, value := range extra {
			options[key] = value
		}
		return query.ProviderRequest{
			Options: options,
			// Newest-first, as a log profile is read. The tail has to invert it.
			Order: query.Order{{Column: "@timestamp", Desc: true}, {Column: "_id", Unique: true}},
		}
	}

	BeforeAll(func() {
		address = strings.TrimSpace(os.Getenv("COMMONS_DB_OPENSEARCH_URL"))
		if address == "" {
			Skip("COMMONS_DB_OPENSEARCH_URL is not set")
		}
		Expect(openSearchAdmin(address, http.MethodDelete, "/"+index, "")).To(Succeed())
		// @timestamp is mapped explicitly so the tail's ordering and any lag
		// bound are resolved against a date rather than a guess.
		Expect(openSearchAdmin(address, http.MethodPut, "/"+index, `{"mappings":{"properties":{
			"@timestamp":{"type":"date"},
			"message":{"type":"keyword"}
		}}}`)).To(Succeed())
	})

	AfterAll(func() {
		if address != "" {
			_ = openSearchAdmin(address, http.MethodDelete, "/"+index, "")
		}
	})

	It("emits the backfill and then documents indexed while it is running", func() {
		base := time.Now().UTC().Add(-time.Minute)
		index_("1", base, "live-a1")
		index_("2", base.Add(time.Second), "live-a2")

		ctx, cancel := tailContext(dbcontext.New())
		rows, done := tail(ctx, streamer("opensearch"), request(nil))

		collected := func() []string {
			var seen []string
			for {
				select {
				case row := <-rows:
					seen = append(seen, fmt.Sprint(row["message"]))
				default:
					return seen
				}
			}
		}

		var seen []string
		Eventually(func() []string {
			seen = append(seen, collected()...)
			return seen
		}, "30s", "100ms").Should(ContainElement("live-a2"))

		// Written after the tail was already caught up, so it can only arrive by
		// being followed — and only after the cluster has refreshed.
		index_("3", time.Now().UTC(), "live-a3")
		Eventually(func() []string {
			seen = append(seen, collected()...)
			return seen
		}, "30s", "100ms").Should(ContainElement("live-a3"))

		// Ascending, and each document exactly once: a cursor that resumed in the
		// wrong place against a real index would repeat the backfill here.
		Expect(seen).To(Equal([]string{"live-a1", "live-a2", "live-a3"}))

		Consistently(done, "300ms").ShouldNot(Receive())
		cancel()
		Eventually(done, "10s", "10ms").Should(Receive(BeNil()))
	})

	// A real cluster is the only place the lag bound can be shown to still
	// return rows: it is compiled against the live mapping of @timestamp, and a
	// bound spelled wrong for that mapping is rejected or matches nothing.
	It("still serves a tail that holds its cursor behind now", func() {
		index_("4", time.Now().UTC().Add(-2*time.Minute), "lagged-a1")

		req := request(map[string]any{"tailLag": "5s"})
		req.Params = map[string]any{"since": "now-1h"}
		req.ParamRoles = map[string]query.ParamRole{"since": query.ParamRoleTimeFrom}

		ctx, cancel := tailContext(dbcontext.New())
		DeferCleanup(cancel)
		rows, _ := tail(ctx, streamer("opensearch"), req)

		var seen []string
		Eventually(func() []string {
			for {
				select {
				case row := <-rows:
					seen = append(seen, fmt.Sprint(row["message"]))
				default:
					return seen
				}
			}
		}, "30s", "100ms").Should(ContainElement("lagged-a1"))
	})
})
