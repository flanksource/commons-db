package providers

import (
	stdctx "context"
	"fmt"
	"time"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query/metrics"
	dbtypes "github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type metricClient struct {
	calls   int
	sizes   []int
	partial bool
}

func (c *metricClient) GetMetricData(_ stdctx.Context, in cloudaws.GetMetricDataInput) (*cloudaws.GetMetricDataOutput, error) {
	c.calls++
	c.sizes = append(c.sizes, len(in.Queries))
	out := &cloudaws.GetMetricDataOutput{}
	for _, q := range in.Queries {
		status := cloudaws.StatusCodeComplete
		if c.partial {
			status = cloudaws.StatusCodePartialData
		}
		out.Results = append(out.Results, cloudaws.MetricDataResult{ID: q.ID, StatusCode: status, Timestamps: []time.Time{time.Unix(0, 0)}, Values: []float64{10}})
	}
	if c.calls == 1 {
		out.NextToken = "next"
	}
	return out, nil
}

var _ = Describe("CloudWatch batches", func() {
	It("excludes points rounded outside the requested time range", func() {
		c := &metricClient{calls: 1}
		count := 0
		err := readCloudwatch(stdctx.Background(), c, []cloudaws.MetricDataQuery{{ID: "m"}}, time.Unix(1, 0), time.Unix(60, 0), func(metrics.Sample) error { count++; return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(BeZero())
	})
	It("uses the supplied session credentials", func() {
		c := connection.AWSConnection{Region: "eu-west-1", AccessKey: dbtypes.EnvVar{ValueStatic: "example-access"}, SecretKey: dbtypes.EnvVar{ValueStatic: "example-secret"}, SessionToken: dbtypes.EnvVar{ValueStatic: "example-session"}}
		session, err := c.Client(context.New())
		Expect(err).NotTo(HaveOccurred())
		credentials, err := session.Credentials().Retrieve(stdctx.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(credentials.SessionToken).To(Equal("example-session"))
	})
	It("walks continuation tokens and splits batches at 500", func() {
		c := &metricClient{}
		queries := make([]cloudaws.MetricDataQuery, 501)
		for i := range queries {
			queries[i].ID = fmt.Sprintf("m%d", i)
		}
		count := 0
		err := readCloudwatch(stdctx.Background(), c, queries, time.Unix(0, 0), time.Unix(60, 0), func(metrics.Sample) error { count++; return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(c.sizes).To(Equal([]int{500, 500, 1}))
		Expect(count).To(Equal(1001))
	})
	It("refuses terminal partial data", func() {
		c := &metricClient{partial: true, calls: 1}
		err := readCloudwatch(stdctx.Background(), c, []cloudaws.MetricDataQuery{{ID: "m"}}, time.Time{}, time.Now(), func(metrics.Sample) error { return nil })
		Expect(err).To(HaveOccurred())
	})
})
