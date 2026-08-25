package observability_test

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Connection logging policy", func() {
	It("resolves SQL defaults and the exact slow boundary", func() {
		policy, err := observability.PolicyFor(&models.Connection{Type: models.ConnectionTypePostgres})
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.SlowThreshold).To(Equal(time.Second))
		Expect(policy.Level(observability.EventError)).To(Equal(logger.Error))
		Expect(policy.Level(observability.EventSlow)).To(Equal(logger.Warn))
		Expect(policy.Level(observability.EventSQL)).To(Equal(logger.Trace))
		Expect(policy.Level(observability.EventSQLParams)).To(Equal(logger.Trace1))
		Expect(policy.Enabled(observability.EventHTTP)).To(BeFalse())

		event, level := policy.Completion(999*time.Millisecond, nil)
		Expect(event).To(Equal(observability.EventSQL))
		Expect(level).To(Equal(logger.Trace))
		event, level = policy.Completion(time.Second, nil)
		Expect(event).To(Equal(observability.EventSlow))
		Expect(level).To(Equal(logger.Warn))
		event, level = policy.Completion(time.Millisecond, errors.New("boom"))
		Expect(event).To(Equal(observability.EventError))
		Expect(level).To(Equal(logger.Error))
	})

	It("resolves the shared HTTP ladder", func() {
		policy, err := observability.PolicyFor(&models.Connection{Type: models.ConnectionTypeOpenSearch})
		Expect(err).NotTo(HaveOccurred())
		Expect(map[observability.Event]logger.LogLevel{
			observability.EventHTTP:             policy.Level(observability.EventHTTP),
			observability.EventHTTPHeaders:      policy.Level(observability.EventHTTPHeaders),
			observability.EventHTTPRequestBody:  policy.Level(observability.EventHTTPRequestBody),
			observability.EventHTTPResponseBody: policy.Level(observability.EventHTTPResponseBody),
		}).To(Equal(map[observability.Event]logger.LogLevel{
			observability.EventHTTP:             logger.Debug,
			observability.EventHTTPHeaders:      logger.Trace,
			observability.EventHTTPRequestBody:  logger.Trace1,
			observability.EventHTTPResponseBody: logger.Trace2,
		}))
	})

	It("uses the connection type in preview examples", func() {
		capability := observability.CapabilityFor(models.ConnectionTypeOpenSearch)
		for _, event := range capability.Events {
			Expect(event.Example).To(SatisfyAll(
				HaveKeyWithValue("event", string(event.Event)),
				HaveKeyWithValue("connection_level", event.Default),
				HaveKeyWithValue("provider", models.ConnectionTypeOpenSearch),
				HaveKey("connection"),
				HaveKey("duration_ms"),
				HaveKey("rows"),
			), "event %s", event.Event)
			encoded, err := json.Marshal(event)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(encoded)).To(ContainSubstring(`"prettyExample":`), "event %s", event.Event)
		}
	})

	It("applies explicit overrides including off", func() {
		policy, err := observability.PolicyFor(&models.Connection{
			Type: models.ConnectionTypeHTTP,
			Properties: types.JSONStringMap{
				observability.PropertySlowThreshold:         "250ms",
				observability.PropertyHTTPLevel:             "info",
				observability.PropertyHTTPHeadersLevel:      "off",
				observability.PropertyHTTPResponseBodyLevel: "trace4",
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.SlowThreshold).To(Equal(250 * time.Millisecond))
		Expect(policy.Level(observability.EventHTTP)).To(Equal(logger.Info))
		Expect(policy.Enabled(observability.EventHTTPHeaders)).To(BeFalse())
		Expect(policy.Level(observability.EventHTTPResponseBody)).To(Equal(logger.Trace4))
	})

	DescribeTable("rejects invalid overrides",
		func(properties types.JSONStringMap, expected string) {
			_, err := observability.PolicyFor(&models.Connection{Type: models.ConnectionTypeHTTP, Properties: properties})
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("level", types.JSONStringMap{observability.PropertyHTTPLevel: "verbose"}, observability.PropertyHTTPLevel),
		Entry("duration", types.JSONStringMap{observability.PropertySlowThreshold: "eventually"}, observability.PropertySlowThreshold),
		Entry("zero duration", types.JSONStringMap{observability.PropertySlowThreshold: "0s"}, "positive"),
	)
})
