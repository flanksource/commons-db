package providers

import (
	stdctx "context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cw "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
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

func (c *metricClient) GetMetricData(_ stdctx.Context, in *cw.GetMetricDataInput, _ ...func(*cw.Options)) (*cw.GetMetricDataOutput, error) {
	c.calls++
	c.sizes = append(c.sizes, len(in.MetricDataQueries))
	out := &cw.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		status := types.StatusCodeComplete
		if c.partial {
			status = types.StatusCodePartialData
		}
		out.MetricDataResults = append(out.MetricDataResults, types.MetricDataResult{Id: q.Id, StatusCode: status, Timestamps: []time.Time{time.Unix(0, 0)}, Values: []float64{10}})
	}
	if c.calls == 1 {
		out.NextToken = aws.String("next")
	}
	return out, nil
}

var _ = Describe("CloudWatch batches", func() {
	It("excludes points rounded outside the requested time range", func() {
		c := &metricClient{calls: 1}
		count := 0
		err := readCloudwatch(stdctx.Background(), c, []types.MetricDataQuery{{Id: aws.String("m")}}, time.Unix(1, 0), time.Unix(60, 0), func(metrics.Sample) error { count++; return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(BeZero())
	})
	It("uses the supplied session credentials", func() {
		c := connection.AWSConnection{Region: "eu-west-1", AccessKey: dbtypes.EnvVar{ValueStatic: "example-access"}, SecretKey: dbtypes.EnvVar{ValueStatic: "example-secret"}, SessionToken: dbtypes.EnvVar{ValueStatic: "example-session"}}
		cfg, err := c.Client(context.New())
		Expect(err).NotTo(HaveOccurred())
		credentials, err := cfg.Credentials.Retrieve(stdctx.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(credentials.SessionToken).To(Equal("example-session"))
	})
	It("walks continuation tokens and splits batches at 500", func() {
		c := &metricClient{}
		queries := make([]types.MetricDataQuery, 501)
		for i := range queries {
			queries[i].Id = aws.String(fmt.Sprintf("m%d", i))
		}
		count := 0
		err := readCloudwatch(stdctx.Background(), c, queries, time.Unix(0, 0), time.Unix(60, 0), func(metrics.Sample) error { count++; return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(c.sizes).To(Equal([]int{500, 500, 1}))
		Expect(count).To(Equal(1001))
	})
	It("refuses terminal partial data", func() {
		c := &metricClient{partial: true, calls: 1}
		err := readCloudwatch(stdctx.Background(), c, []types.MetricDataQuery{{Id: aws.String("m")}}, time.Time{}, time.Now(), func(metrics.Sample) error { return nil })
		Expect(err).To(HaveOccurred())
	})
})
