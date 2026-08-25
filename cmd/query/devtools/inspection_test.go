package devtools_test

import (
	gocontext "context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	inspection "github.com/flanksource/commons-db/inspect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func remove(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, target, nil))
	return recorder
}

func decode[T any](recorder *httptest.ResponseRecorder) T {
	var body T
	Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(Succeed())
	return body
}

var _ = Describe("Handler inspection caches", func() {
	var handler http.Handler

	// A memo with a policy name no other spec uses, so flushing it cannot
	// disturb the caches every other package registered into the same process.
	fill := func(name string, keys ...string) *inspection.Memo[string] {
		memo := inspection.NewMemo(inspection.MemoOptions[string]{
			Policy: inspection.CachePolicy{
				Name: name, InitialFreshFor: time.Hour, MaximumFreshFor: time.Hour,
				FillTimeout: time.Second, MaxEntries: 8, MaxWeight: 8,
			},
		})
		for _, key := range keys {
			_, err := memo.Get(gocontext.Background(), inspection.GetOptions[string]{
				Key: key, Load: func(gocontext.Context) (string, error) { return "catalog", nil },
			})
			Expect(err).ToNot(HaveOccurred())
		}
		return memo
	}

	BeforeEach(func() {
		handler, _ = newHandler(devtools.NewStore(devtools.Options{}), true)
	})

	It("describes what the caches are holding against the ceilings they have", func() {
		fill("handler-listed", "a", "b")

		body := decode[devtools.InspectionCaches](get(handler, "/api/v1/devtools/inspection"))

		var listed *inspection.CacheStats
		for index, stats := range body.Caches {
			if stats.Policy == "handler-listed" {
				listed = &body.Caches[index]
			}
		}
		Expect(listed).ToNot(BeNil())
		Expect(listed.Entries).To(Equal(2))
		Expect(listed.MaxEntries).To(Equal(8))
		Expect(listed.FreshForSeconds).To(Equal(int64(3600)))
	})

	It("flushes one cache and reports what went", func() {
		memo := fill("handler-flushed", "a", "b", "c")

		response := remove(handler, "/api/v1/devtools/inspection?policy=handler-flushed")

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(decode[inspection.FlushResult](response)).To(Equal(inspection.FlushResult{
			Caches:  []inspection.FlushedCache{{Policy: "handler-flushed", Entries: 3}},
			Entries: 3,
		}))
		Expect(memo.Stats().Entries).To(BeZero())
	})

	It("flushes a single key", func() {
		memo := fill("handler-keyed", "keep", "drop")

		response := remove(handler, "/api/v1/devtools/inspection?policy=handler-keyed&key=drop")

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(decode[inspection.FlushResult](response).Entries).To(Equal(1))
		Expect(memo.Stats().Entries).To(Equal(1))
	})

	// A typo would otherwise drop nothing and answer 200, which reads as a cache
	// that refuses to clear rather than as a name that does not exist.
	It("refuses a cache name it does not have, and lists the ones it does", func() {
		fill("handler-known")

		response := remove(handler, "/api/v1/devtools/inspection?policy=handler-unknown")

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`unknown inspection cache "handler-unknown"`))
		Expect(response.Body.String()).To(ContainSubstring("handler-known"))
	})

	It("says nothing was dropped rather than implying something was", func() {
		fill("handler-already-empty")

		response := remove(handler, "/api/v1/devtools/inspection?policy=handler-already-empty")

		result := decode[inspection.FlushResult](response)
		Expect(result.Entries).To(BeZero())
		Expect(result.Caches).To(BeEmpty())
	})

	It("keeps the whole surface behind the same gate as the rest", func() {
		disabled, _ := newHandler(devtools.NewStore(devtools.Options{}), false)

		Expect(get(disabled, "/api/v1/devtools/inspection").Code).To(Equal(http.StatusNotFound))
		Expect(remove(disabled, "/api/v1/devtools/inspection").Code).To(Equal(http.StatusNotFound))
	})

	It("clears the arming headers through preflight", func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodOptions, "/api/v1/devtools", nil))

		allowed := recorder.Header().Get("Access-Control-Allow-Headers")
		Expect(allowed).To(ContainSubstring(devtools.LevelHeader))
		Expect(allowed).To(ContainSubstring(devtools.RefreshHeader))
	})
})

var _ = Describe("Arming a refresh", func() {
	armed := func(value string) bool {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/orders", nil)
		if value != "" {
			request.Header.Set(devtools.RefreshHeader, value)
		}
		return devtools.RefreshRequested(request)
	}

	It("reads the header a console sets", func() {
		Expect(armed("true")).To(BeTrue())
		Expect(armed("1")).To(BeTrue())
		Expect(armed("yes")).To(BeTrue())
	})

	// Anything else is off. A misspelt value turning every page into a cache
	// miss is a far worse failure than one that quietly does nothing.
	It("treats an absent or unrecognised value as off", func() {
		Expect(armed("")).To(BeFalse())
		Expect(armed("false")).To(BeFalse())
		Expect(armed("please")).To(BeFalse())
	})

	It("carries the request's answer onto the recorder it mints", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/orders", nil)
		request.Header.Set(devtools.LevelHeader, "debug")
		request.Header.Set(devtools.RefreshHeader, "true")

		recorder, err := devtools.NewRequestRecorder(devtools.ArmOptions{Request: request})

		Expect(err).ToNot(HaveOccurred())
		Expect(recorder.RefreshInspection()).To(BeTrue())
	})

	It("leaves it off for a request that only asked to be recorded", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/orders", nil)
		request.Header.Set(devtools.LevelHeader, "debug")

		recorder, err := devtools.NewRequestRecorder(devtools.ArmOptions{Request: request})

		Expect(err).ToNot(HaveOccurred())
		Expect(recorder.RefreshInspection()).To(BeFalse())
	})
})
