package context_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Request observability", func() {
	It("raises HAR capture for one request without raising HTTP log output", func() {
		ctx := context.New().WithRequestHARLevel(logger.Trace)

		harLevel, harSource := ctx.EffectiveHARLevel("http")
		logLevel, logSource := ctx.EffectiveLogLevel("http")
		Expect(harLevel).To(Equal(logger.Trace))
		Expect(harSource).To(Equal("request"))
		Expect(logLevel).To(Equal(logger.Info))
		Expect(logSource).To(Equal("logger"))
	})
})
