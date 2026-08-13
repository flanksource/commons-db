package connections

import (
	"strings"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection dashboard model", func() {
	It("keeps the endpoint identity without exposing URL credentials", func() {
		endpoint := dashboardEndpointFor(&models.Connection{
			Type: models.ConnectionTypeClickHouse,
			URL:  "clickhouse://reporter:hunter2@warehouse.example:9000/analytics?token=secret",
		})

		Expect(endpoint).To(Equal(&dashboardEndpoint{
			Scheme: "clickhouse",
			Host:   "warehouse.example:9000",
			Path:   "/analytics",
		}))
		Expect(strings.Contains(endpoint.Host+endpoint.Path, "hunter2")).To(BeFalse())
	})

	It("normalizes SQL Server keyword connection strings", func() {
		endpoint := dashboardEndpointFor(&models.Connection{
			Type: models.ConnectionTypeSQLServer,
			URL:  "server=mssql.example;user id=sa;password=hunter2;database=APP_QA;port=31433",
		})

		Expect(endpoint).To(Equal(&dashboardEndpoint{
			Scheme: "sqlserver",
			Host:   "mssql.example:31433",
			Path:   "/APP_QA",
		}))
	})

	It("counts plain and namespaced profile references against the matching connection", func() {
		counts := profileUsageCounts([]query.Profile{
			{Name: "plain", Provider: query.ProviderConfig{Connection: "connection://warehouse"}},
			{Name: "scoped", Provider: query.ProviderConfig{Connection: "connection://acme/warehouse"}},
			{Name: "inline", Provider: query.ProviderConfig{Connection: "postgres://example.test/db"}},
		})

		Expect(dashboardProfileCount(&models.Connection{Name: "warehouse", Namespace: "acme"}, counts)).To(Equal(2))
		Expect(dashboardProfileCount(&models.Connection{Name: "warehouse", Namespace: "other"}, counts)).To(Equal(1))
	})

	It("documents the inventory read and its opt-in health trigger in OpenAPI", func() {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}}
		AddConnectionsOpenAPI(spec)

		Expect(spec.Paths[connectionDashboardPath]["get"].Summary).To(Equal("List connection inventory"))
		Expect(spec.Paths[connectionHealthPath]["post"].Summary).To(Equal("Check connection health"))
		Expect(spec.Paths[connectionHealthPath]["get"]).To(BeZero(),
			"health checks must be opt-in, so the inventory verb must not probe")
	})
})
