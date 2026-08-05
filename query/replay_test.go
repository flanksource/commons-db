package query_test

import (
	gocontext "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func replayCtx() dbcontext.Context {
	return dbcontext.NewContext(gocontext.Background()).WithNamespace("default")
}

// ledgerRow is the row the replay expressions read: a nested payload, a path,
// and an identity that ends up in a header.
func ledgerRow() query.Row {
	return query.Row{
		"path":    "/ledger",
		"tenant":  "za",
		"payload": map[string]any{"id": "GL-1"},
	}
}

func ledgerProfile(target connection.HTTPConnection) query.Profile {
	return query.Profile{
		Name:    "d365.replay",
		Columns: []query.ColumnDef{{Name: "path"}, {Name: "tenant"}},
		Replay: &query.ReplaySpec{
			Kind:   query.ReplayKindHTTP,
			Target: target,
			Method: `"POST"`,
			URL:    `path`,
			Body:   `payload`,
			Headers: map[string]string{
				"Content-Type":    `"application/json"`,
				"LegalEntityCode": `tenant`,
				"Authorization":   `"Bearer super-secret"`,
				"X-Blank":         `""`,
			},
		},
	}
}

var _ = Describe("BuildReplayPreview", func() {
	target := connection.HTTPConnection{URL: "https://api.example.test/base"}

	build := func(mutate func(*query.ReplayBuildOptions)) (*query.ReplayPreview, error) {
		opts := query.ReplayBuildOptions{
			Profile: ledgerProfile(target),
			Rows:    []query.Row{ledgerRow()},
		}
		if mutate != nil {
			mutate(&opts)
		}
		return query.BuildReplayPreview(replayCtx(), opts)
	}

	It("resolves the method, url, body and headers from CEL", func() {
		preview, err := build(nil)
		Expect(err).ToNot(HaveOccurred())

		Expect(preview.Method).To(Equal("POST"))
		Expect(preview.URL).To(Equal("https://api.example.test/base/ledger"))
		Expect(preview.BodyPreview).To(Equal(`{"id":"GL-1"}`))
		Expect(preview.Headers).To(HaveKeyWithValue("LegalEntityCode", "za"))
	})

	It("drops a header whose expression yields blank rather than sending it empty", func() {
		preview, err := build(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.Headers).ToNot(HaveKey("X-Blank"))
	})

	It("masks sensitive headers in the preview but still sends them", func() {
		preview, err := build(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.Headers).To(HaveKeyWithValue("Authorization", "********"))
	})

	It("strips credentials from the displayed url", func() {
		preview, err := build(func(o *query.ReplayBuildOptions) {
			o.TargetOverride = "https://user:hunter2@api.example.test"
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.URL).To(Equal("https://api.example.test/ledger"))
		Expect(preview.Target).To(Equal("https://api.example.test"))
	})

	It("defaults the method to POST when the profile declares none", func() {
		preview, err := build(func(o *query.ReplayBuildOptions) { o.Profile.Replay.Method = "" })
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.Method).To(Equal(http.MethodPost))
	})

	It("lets every override win over the profile expression", func() {
		preview, err := build(func(o *query.ReplayBuildOptions) {
			o.MethodOverride = "put"
			o.URLOverride = "/override"
			o.BodyOverride = "raw"
			o.Headers = map[string]string{"LegalEntityCode": "uk"}
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(preview.Method).To(Equal("PUT"))
		Expect(preview.URL).To(Equal("https://api.example.test/base/override"))
		Expect(preview.BodyPreview).To(Equal("raw"))
		Expect(preview.Headers).To(HaveKeyWithValue("LegalEntityCode", "uk"))
	})

	It("falls back to the origin of an absolute url when no target is configured", func() {
		preview, err := build(func(o *query.ReplayBuildOptions) {
			o.Profile.Replay.Target = connection.HTTPConnection{}
			o.Profile.Replay.URL = `"https://elsewhere.test/deep/path?x=1"`
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.Target).To(Equal("https://elsewhere.test"))
		Expect(preview.URL).To(Equal("https://elsewhere.test/deep/path?x=1"))
	})

	It("refuses a relative url with no target to resolve it against", func() {
		_, err := build(func(o *query.ReplayBuildOptions) {
			o.Profile.Replay.Target = connection.HTTPConnection{}
		})
		Expect(err).To(MatchError(ContainSubstring("replay target is required")))
	})

	It("refuses a url expression that resolves empty", func() {
		_, err := build(func(o *query.ReplayBuildOptions) { o.Profile.Replay.URL = `""` })
		Expect(err).To(MatchError(ContainSubstring("replay url resolved empty")))
	})

	It("refuses a replay kind it cannot send", func() {
		_, err := build(func(o *query.ReplayBuildOptions) { o.Profile.Replay.Kind = "grpc" })
		Expect(err).To(MatchError(ContainSubstring(`kind "grpc" is not supported`)))
	})

	It("truncates a body past the preview cap but reports its full size", func() {
		long := make([]byte, 100)
		for i := range long {
			long[i] = 'x'
		}
		preview, err := build(func(o *query.ReplayBuildOptions) {
			o.BodyOverride = string(long)
			o.MaxBodyPreview = 10
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.BodyTruncated).To(BeTrue())
		Expect(preview.BodyPreview).To(HaveLen(10))
		Expect(preview.BodyBytes).To(Equal(100))
	})

	It("describes the selected row using the profile's visible columns", func() {
		preview, err := build(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.Row.Values).To(Equal(map[string]any{"path": "/ledger", "tenant": "za"}))
	})
})

var _ = Describe("replay row selection", func() {
	target := connection.HTTPConnection{URL: "https://api.example.test"}
	rows := []query.Row{
		{"path": "/a", "id": "1", "payload": "one"},
		{"path": "/b", "id": "2", "payload": "two"},
		{"path": "/c", "id": "2", "payload": "three"},
	}

	build := func(selector map[string]string) (*query.ReplayPreview, error) {
		return query.BuildReplayPreview(replayCtx(), query.ReplayBuildOptions{
			Profile: query.Profile{
				Name:   "multi",
				Replay: &query.ReplaySpec{Target: target, URL: `path`, Body: `payload`},
			},
			Rows:   rows,
			Select: selector,
		})
	}

	It("selects the single row matching the selector", func() {
		preview, err := build(map[string]string{"id": "1"})
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.URL).To(Equal("https://api.example.test/a"))
	})

	It("narrows on every selector key together", func() {
		preview, err := build(map[string]string{"id": "2", "path": "/c"})
		Expect(err).ToNot(HaveOccurred())
		Expect(preview.BodyPreview).To(Equal("three"))
	})

	It("refuses an ambiguous selection instead of picking the first match", func() {
		_, err := build(map[string]string{"id": "2"})
		Expect(err).To(MatchError(ContainSubstring("2 rows matched id=2")))
		Expect(err).To(MatchError(ContainSubstring("rows 1, 2")))
	})

	It("reports a selector that matches nothing", func() {
		_, err := build(map[string]string{"id": "99"})
		Expect(err).To(MatchError(ContainSubstring("no row matched id=99")))
	})

	It("reports an empty result", func() {
		_, err := query.BuildReplayPreview(replayCtx(), query.ReplayBuildOptions{
			Profile: query.Profile{Name: "empty", Replay: &query.ReplaySpec{Target: target, URL: `"/x"`}},
		})
		Expect(err).To(MatchError(ContainSubstring("no rows to replay")))
	})
})

var _ = Describe("replay preview hash", func() {
	target := connection.HTTPConnection{URL: "https://api.example.test"}

	hashOf := func(mutate func(*query.ReplayBuildOptions)) string {
		GinkgoHelper()
		opts := query.ReplayBuildOptions{Profile: ledgerProfile(target), Rows: []query.Row{ledgerRow()}}
		if mutate != nil {
			mutate(&opts)
		}
		preview, err := query.BuildReplayPreview(replayCtx(), opts)
		Expect(err).ToNot(HaveOccurred())
		return preview.Hash
	}

	It("is stable across identical builds", func() {
		Expect(hashOf(nil)).To(Equal(hashOf(nil)))
	})

	DescribeTable("changes when anything that would be sent changes",
		func(mutate func(*query.ReplayBuildOptions)) {
			Expect(hashOf(mutate)).ToNot(Equal(hashOf(nil)))
		},
		Entry("the method", func(o *query.ReplayBuildOptions) { o.MethodOverride = "PUT" }),
		Entry("the url", func(o *query.ReplayBuildOptions) { o.URLOverride = "/other" }),
		Entry("the body", func(o *query.ReplayBuildOptions) { o.BodyOverride = "changed" }),
		Entry("a header", func(o *query.ReplayBuildOptions) { o.Headers = map[string]string{"X-New": "1"} }),
		Entry("the row it was built from", func(o *query.ReplayBuildOptions) {
			o.Rows = []query.Row{{"path": "/other", "tenant": "za", "payload": map[string]any{"id": "GL-2"}}}
		}),
	)
})

var _ = Describe("ExecuteReplay", func() {
	It("sends the request and captures the response", func() {
		var (
			gotMethod string
			gotPath   string
			gotAuth   string
			gotBody   string
		)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotMethod, gotPath, gotAuth, gotBody = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}))
		DeferCleanup(server.Close)

		ctx := replayCtx()
		preview, err := query.BuildReplayPreview(ctx, query.ReplayBuildOptions{
			Profile: ledgerProfile(connection.HTTPConnection{URL: server.URL}),
			Rows:    []query.Row{ledgerRow()},
		})
		Expect(err).ToNot(HaveOccurred())

		result, err := query.ExecuteReplay(ctx, preview)
		Expect(err).ToNot(HaveOccurred())

		Expect(gotMethod).To(Equal(http.MethodPost))
		Expect(gotPath).To(Equal("/ledger"))
		// Masked in the preview, sent in full on the wire.
		Expect(gotAuth).To(Equal("Bearer super-secret"))
		Expect(gotBody).To(Equal(`{"id":"GL-1"}`))

		Expect(result.StatusCode).To(Equal(http.StatusCreated))
		Expect(result.ResponsePreview).To(ContainSubstring(`"status":"ok"`))
		Expect(result.ResponseHeaders).To(HaveKeyWithValue("Content-Type", "application/json"))
	})

	It("surfaces a non-2xx response as a result, not an error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusBadRequest)
		}))
		DeferCleanup(server.Close)

		ctx := replayCtx()
		preview, err := query.BuildReplayPreview(ctx, query.ReplayBuildOptions{
			Profile: ledgerProfile(connection.HTTPConnection{URL: server.URL}),
			Rows:    []query.Row{ledgerRow()},
		})
		Expect(err).ToNot(HaveOccurred())

		result, err := query.ExecuteReplay(ctx, preview)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.StatusCode).To(Equal(http.StatusBadRequest))
	})

	It("refuses a method it cannot send", func() {
		ctx := replayCtx()
		preview, err := query.BuildReplayPreview(ctx, query.ReplayBuildOptions{
			Profile:        ledgerProfile(connection.HTTPConnection{URL: "https://api.example.test"}),
			Rows:           []query.Row{ledgerRow()},
			MethodOverride: "TRACE",
		})
		Expect(err).ToNot(HaveOccurred())

		_, err = query.ExecuteReplay(ctx, preview)
		Expect(err).To(MatchError(ContainSubstring("unsupported replay method")))
	})
})

var _ = Describe("MergeReplaySpec", func() {
	base := &query.ReplaySpec{
		Kind:    query.ReplayKindHTTP,
		Target:  connection.HTTPConnection{URL: "https://base.test"},
		Method:  `"POST"`,
		URL:     `path`,
		Body:    `payload`,
		Headers: map[string]string{"Accept": `"application/json"`, "X-Env": `"base"`},
	}

	It("overlays only the fields the importing profile sets", func() {
		merged := query.MergeReplaySpec(base, &query.ReplaySpec{
			Target:  connection.HTTPConnection{URL: "https://override.test"},
			Headers: map[string]string{"X-Env": `"override"`},
		})

		Expect(merged.Target.URL).To(Equal("https://override.test"))
		Expect(merged.Method).To(Equal(`"POST"`))
		Expect(merged.Headers).To(HaveKeyWithValue("Accept", `"application/json"`))
		Expect(merged.Headers).To(HaveKeyWithValue("X-Env", `"override"`))
	})

	It("does not mutate the stored base spec", func() {
		query.MergeReplaySpec(base, &query.ReplaySpec{Headers: map[string]string{"X-Env": `"override"`}})
		Expect(base.Headers).To(HaveKeyWithValue("X-Env", `"base"`))
	})

	It("returns the side that exists when the other is nil", func() {
		Expect(query.MergeReplaySpec(nil, base).Method).To(Equal(`"POST"`))
		Expect(query.MergeReplaySpec(base, nil).Method).To(Equal(`"POST"`))
		Expect(query.MergeReplaySpec(nil, nil)).To(BeNil())
	})
})
