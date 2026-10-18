package devtools_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func armRequest(target string, header string) (dbcontext.Context, *query.Recorder, error) {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if header != "" {
		request.Header.Set(devtools.LevelHeader, header)
	}
	return devtools.Arm(devtools.ArmOptions{
		Context: dbcontext.New(),
		Request: request,
		Source:  query.ExecutionSource{Surface: "profile", Profile: "orders"},
		NewID:   func() string { return "fixed-id" },
	})
}

var _ = Describe("Arm", func() {
	// The unarmed path is nearly every request the server serves. It must cost a
	// header read and nothing else.
	It("returns the context untouched when nothing asked for capture", func() {
		ctx, recorder, err := armRequest("/api/v1/profile/orders", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(recorder).To(BeNil())
		Expect(query.RecorderFrom(ctx)).To(BeNil())
	})

	It("treats an explicit off the same as absent", func() {
		_, recorder, err := armRequest("/api/v1/profile/orders", "off")
		Expect(err).ToNot(HaveOccurred())
		Expect(recorder).To(BeNil())
	})

	It("arms at the level asked for and puts the recorder on the context", func() {
		ctx, recorder, err := armRequest("/api/v1/profile/orders", "trace2")
		Expect(err).ToNot(HaveOccurred())
		Expect(recorder.Level()).To(Equal(logger.Trace2))
		Expect(recorder.ID()).To(Equal("fixed-id"))
		Expect(query.RecorderFrom(ctx)).To(BeIdenticalTo(recorder))
	})

	// A client that sent "verbose" and silently got "info" would report a bug
	// against the wrong layer.
	It("rejects a level it does not know rather than defaulting", func() {
		_, recorder, err := armRequest("/api/v1/profile/orders", "verbose")
		Expect(err).To(MatchError(ContainSubstring(`unknown X-Debug-Level "verbose"`)))
		Expect(err).To(MatchError(ContainSubstring("trace2")), "the error lists what it would have accepted")
		Expect(recorder).To(BeNil())
	})

	It("accepts the query parameter for a caller that cannot set a header", func() {
		_, recorder, err := armRequest("/api/v1/profile/orders?__debug=debug", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(recorder.Level()).To(Equal(logger.Debug))
	})

	It("records the request line so a console can offer to re-run it", func() {
		_, recorder, err := armRequest("/api/v1/profile/orders?region=EU&__debug=debug", "")
		Expect(err).ToNot(HaveOccurred())

		source := recorder.Summary().Source
		Expect(source.Method).To(Equal(http.MethodGet))
		Expect(source.Path).To(Equal("/api/v1/profile/orders"))
		Expect(source.Query).To(Equal("region=EU"), "the arming marker is not part of the request being explained")
	})

	It("blanks a credential-shaped parameter out of the recorded request line", func() {
		_, recorder, err := armRequest("/api/v1/profile/orders?token=hunter2&region=EU", "debug")
		Expect(err).ToNot(HaveOccurred())

		Expect(recorder.Summary().Source.Query).To(ContainSubstring("region=EU"))
		Expect(recorder.Summary().Source.Query).ToNot(ContainSubstring("hunter2"))
	})
})

var _ = Describe("StampID", func() {
	It("names nothing when there is no record", func() {
		header := http.Header{}
		devtools.StampID(header, "")
		Expect(header).To(BeEmpty())
	})

	It("writes the id and exposes it to a browser", func() {
		header := http.Header{}
		devtools.StampID(header, "rec-1")

		Expect(header.Get(devtools.IDHeader)).To(Equal("rec-1"))
		Expect(header.Get("Access-Control-Expose-Headers")).To(Equal(devtools.IDHeader))
	})

	// Downstream handlers add their own headers to the expose list rather than
	// replacing it, so the id exposed up front stays readable.
	It("survives a downstream handler exposing its own headers", func() {
		header := http.Header{}
		devtools.StampID(header, "rec-1")
		entity.ExposeHeaders(header, "X-Total-Count", "x-debug-id")

		Expect(header.Get("Access-Control-Expose-Headers")).To(Equal(devtools.IDHeader + ", X-Total-Count"))
	})
})
