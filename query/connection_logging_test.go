package query_test

import (
	"bytes"
	"encoding/json"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type connectionLoggingProvider struct {
	typ     string
	details map[string]any
	last    query.ProviderRequest
}

func (p *connectionLoggingProvider) Type() string { return p.typ }

func (p *connectionLoggingProvider) Execute(_ dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	p.last = req
	req.Diagnostics.RecordRequest(req.Query, nil, p.details)
	return []query.Row{{"id": 7}}, nil
}

var _ = Describe("Connection logging", func() {
	It("uses the stored connection policy and attaches bounded diagnostics", func() {
		provider := &connectionLoggingProvider{typ: "connection-logging-slow"}
		query.RegisterProvider(provider)
		buffer := logger.NewBufferedLogger(20)
		buffer.SetLogLevel(logger.Trace4)
		ctx := dbcontext.New(commons.WithLogger(buffer)).WithConnectionResolver(func(string) (*models.Connection, error) {
			return &models.Connection{
				Name: "warehouse",
				Type: models.ConnectionTypePostgres,
				Properties: types.JSONStringMap{
					observability.PropertySlowThreshold: "1ns",
				},
			}, nil
		})

		result, err := query.Execute(ctx, query.Profile{
			Name: "orders",
			Provider: query.ProviderConfig{
				Type:       provider.typ,
				Connection: "connection://warehouse",
			},
			Query: "select * from orders",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(provider.last.Diagnostics).NotTo(BeNil())
		Expect(buffer.GetLogs()).To(HaveLen(1))
		Expect(buffer.GetLogs()[0].Message).To(And(
			ContainSubstring("[connection-logging-slow/warehouse]"),
			ContainSubstring("SLOW SQL >= "),
			ContainSubstring("[rows:1]"),
			ContainSubstring("select * from orders"),
		))
	})

	It("emits structured top-level fields when the active logger is JSON", func() {
		previousJSON := properties.Get("log.json")
		properties.Set("log.json", "true")
		DeferCleanup(properties.Set, "log.json", previousJSON)

		provider := &connectionLoggingProvider{
			typ: "connection-logging-json",
			details: map[string]any{
				"namespace": "prod",
				"pods":      []string{"prod/cache-0"},
				"start":     "2026-08-16T09:00:00Z",
			},
		}
		query.RegisterProvider(provider)
		var output bytes.Buffer
		log := logger.NewWithWriter(&output)
		log.SetLogLevel(logger.Trace4)
		ctx := dbcontext.New(commons.WithLogger(log))

		result, err := query.Execute(ctx, query.Profile{
			Name: "cache entries",
			Provider: query.ProviderConfig{
				Type: provider.typ,
			},
			Query: "scan cache",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))

		var record map[string]any
		Expect(json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record)).To(Succeed())
		Expect(record).To(SatisfyAll(
			HaveKeyWithValue("msg", "connection"),
			HaveKeyWithValue("event", "operation"),
			HaveKeyWithValue("connection_level", "debug"),
			HaveKeyWithValue("provider", provider.typ),
			HaveKeyWithValue("connection", "inline"),
			HaveKeyWithValue("rows", float64(1)),
			HaveKey("duration_ms"),
			HaveKeyWithValue("query", "scan cache"),
			HaveKeyWithValue("filters", map[string]any{
				"namespace": "prod",
				"pods":      []any{"prod/cache-0"},
				"start":     "2026-08-16T09:00:00Z",
			}),
		))
		Expect(strings.TrimSpace(output.String())).NotTo(ContainSubstring("event="))
	})

	It("does not emit a disabled provider completion record", func() {
		provider := &connectionLoggingProvider{typ: "connection-logging-off"}
		query.RegisterProvider(provider)
		buffer := logger.NewBufferedLogger(20)
		buffer.SetLogLevel(logger.Trace4)
		ctx := dbcontext.New(commons.WithLogger(buffer)).WithConnectionResolver(func(string) (*models.Connection, error) {
			return &models.Connection{
				Name: "warehouse",
				Type: models.ConnectionTypePostgres,
				Properties: types.JSONStringMap{
					observability.PropertySQLLevel: "off",
				},
			}, nil
		})

		_, err := query.Execute(ctx, query.Profile{
			Name: "orders",
			Provider: query.ProviderConfig{
				Type:       provider.typ,
				Connection: "connection://warehouse",
			},
			Query: "select * from orders",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(buffer.GetLogs()).To(BeEmpty())
	})
})
