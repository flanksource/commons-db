// Specs for results routed per tenant: a stream id two routes share stays two
// streams in the index, and a read may span the routes the caller was granted.
package recordresultse2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

const streamsProfile = "/api/v1/profile/profile-trace-results-sample-event-streams"

var _ = Describe("record results routed per tenant", func() {
	var server followServer

	// serveRoutes routes tenant a to a sqlite file and every other tenant to
	// an in-process kv store of its own.
	serveRoutes := func() followServer {
		schemas := recordstore.NewSchemas()
		dir := GinkgoT().TempDir()
		router, err := recordstore.NewRouter(recordstore.RouterOptions{
			Route: recordresultstest.TenantOf,
			Open: func(_ context.Context, route string) (recordstore.Backend, error) {
				if route == "a" {
					if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
						return nil, err
					}
					return sqlite.Open(sqlite.Options{
						Path: filepath.Join(dir, "a", "records.sqlite"), Schema: schemas.Kind, TTL: time.Hour, SweepInterval: time.Hour,
					})
				}
				return recordresultstest.NewKV(schemas), nil
			},
		})
		Expect(err).ToNot(HaveOccurred())
		return newFollowServerWith(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(""),
			Source: router, Schemas: schemas, Register: registerFollowTypes,
		})
	}
	appendAs := func(tenant string, first, last int) {
		_, err := recordstore.AppendTyped(recordresultstest.ForTenant(tenant), server.results.Backend, "run-1", "sample_event",
			recordresultstest.SampleEvents(first, last))
		Expect(err).ToNot(HaveOccurred())
	}
	rowsAs := func(tenant, routes, target string) (int, []map[string]any) {
		header := http.Header{"Accept": {"application/json"}, tenantHeader: {tenant}}
		if routes != "" {
			header.Set(routesHeader, routes)
		}
		response := server.do(http.MethodGet, target, header)
		var rows []map[string]any
		if response.StatusCode == http.StatusOK {
			Expect(json.NewDecoder(response.Body).Decode(&rows)).To(Succeed())
		}
		return response.StatusCode, rows
	}

	BeforeEach(func() {
		server = serveRoutes()
		appendAs("a", 1, 3)
		appendAs("b", 10, 11)
		appendAs("c", 20, 20)
	})

	It("keeps a follow of one route's stream while another route's stream of the same id is read", func() {
		header := http.Header{tenantHeader: {"a"}}
		response := server.do(http.MethodPost, followedProfile+"/sessions?follow=true&stream=run-1&afterSeq=3", header)
		Expect(response.StatusCode).To(Equal(http.StatusCreated))
		var info query.SessionInfo
		Expect(json.NewDecoder(response.Body).Decode(&info)).To(Succeed())
		events, disconnect := server.subscribe(info.ID, "")
		DeferCleanup(disconnect)

		appendAs("b", 12, 15)
		status, rows := rowsAs("b", "", followedProfile+"?stream=run-1")
		Expect(status).To(Equal(http.StatusOK))
		Expect(rows).To(HaveLen(6))
		appendAs("a", 4, 4)

		followed, _, _ := followedFrom(events, 1)
		Expect(followed[0]["seq"]).To(BeEquivalentTo(4))
		Expect(followed[0]["elapsed_ms"]).To(BeEquivalentTo(4))
		status, rows = rowsAs("a", "", followedProfile+"?stream=run-1")
		Expect(status).To(Equal(http.StatusOK))
		Expect(rows).To(HaveLen(4))
	})

	It("reads the streams of the routes a caller was granted, merged newest first", func() {
		status, rows := rowsAs("a", "b", streamsProfile+"?streams=a:run-1,b:run-1")
		Expect(status).To(Equal(http.StatusOK))
		var order []string
		for _, row := range rows {
			order = append(order, fmt.Sprintf("%v#%v", row["stream_id"], row["seq"]))
		}
		Expect(order).To(Equal([]string{"b:run-1#2", "b:run-1#1", "a:run-1#3", "a:run-1#2", "a:run-1#1"}), "newest first across both routes")
	})

	It("refuses a route the caller was not granted, and never serves one it did not list", func() {
		status, _ := rowsAs("a", "b", streamsProfile+"?streams=a:run-1,c:run-1")
		Expect(status).To(Equal(http.StatusForbidden))

		status, rows := rowsAs("a", "b,c", streamsProfile+"?streams=a:run-1,b:run-1")
		Expect(status).To(Equal(http.StatusOK))
		for _, row := range rows {
			Expect(row["stream_id"]).ToNot(Equal("c:run-1"))
		}
	})

	It("reads its own route's streams with no grant at all", func() {
		status, rows := rowsAs("b", "", streamsProfile+"?streams=b:run-1")
		Expect(status).To(Equal(http.StatusOK))
		Expect(rows).To(HaveLen(2))
	})
})
