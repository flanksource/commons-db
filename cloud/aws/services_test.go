package aws_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
)

func readForm(r *http.Request) url.Values {
	GinkgoHelper()
	body, err := io.ReadAll(r.Body)
	Expect(err).ToNot(HaveOccurred())
	form, err := url.ParseQuery(string(body))
	Expect(err).ToNot(HaveOccurred())
	return form
}

var _ = Describe("STS", func() {
	It("posts a signed GetCallerIdentity query and reads the identity", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "sts")
			Expect(r.Method).To(Equal("POST"))
			Expect(readForm(r)).To(Equal(url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}))
			_, _ = w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/example</Arn><UserId>UID</UserId><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`))
		})
		identity, err := session.STS().GetCallerIdentity(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(*identity).To(Equal(cloudaws.CallerIdentity{Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/example", UserID: "UID"}))
	})
})

var _ = Describe("KMS", func() {
	It("encrypts and decrypts with JSON 1.1 targets and base64 blobs", func(ctx SpecContext) {
		var targets []string
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "kms")
			Expect(r.Header.Get("Content-Type")).To(Equal("application/x-amz-json-1.1"))
			targets = append(targets, r.Header.Get("X-Amz-Target"))
			var in map[string]string
			Expect(json.NewDecoder(r.Body).Decode(&in)).To(Succeed())
			if r.Header.Get("X-Amz-Target") == "TrentService.Encrypt" {
				Expect(in).To(Equal(map[string]string{"KeyId": "alias/example", "Plaintext": "c2VjcmV0"}))
				_, _ = w.Write([]byte(`{"CiphertextBlob":"Y2lwaGVy"}`))
				return
			}
			Expect(in).To(Equal(map[string]string{"CiphertextBlob": "Y2lwaGVy"}))
			_, _ = w.Write([]byte(`{"Plaintext":"c2VjcmV0"}`))
		})
		kms := session.KMS()
		ciphertext, err := kms.Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: "alias/example", Plaintext: []byte("secret")})
		Expect(err).ToNot(HaveOccurred())
		Expect(ciphertext).To(Equal([]byte("cipher")))
		plaintext, err := kms.Decrypt(ctx, cloudaws.KMSDecryptInput{CiphertextBlob: ciphertext})
		Expect(err).ToNot(HaveOccurred())
		Expect(plaintext).To(Equal([]byte("secret")))
		Expect(targets).To(Equal([]string{"TrentService.Encrypt", "TrentService.Decrypt"}))
	})
})

var _ = Describe("EKS", func() {
	It("describes a cluster over REST-JSON", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "eks")
			Expect(r.Method + " " + r.URL.Path).To(Equal("GET /clusters/prod"))
			_, _ = w.Write([]byte(`{"cluster":{"endpoint":"https://k.example","certificateAuthority":{"data":"Q0E="}}}`))
		})
		cluster, err := session.EKS().DescribeCluster(ctx, "prod")
		Expect(err).ToNot(HaveOccurred())
		Expect(*cluster).To(Equal(cloudaws.EKSCluster{Endpoint: "https://k.example", CertificateAuthorityData: "Q0E="}))
	})
})

var _ = Describe("CloudWatch Logs", func() {
	It("starts a query and reads its results", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "logs")
			var in map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&in)).To(Succeed())
			switch r.Header.Get("X-Amz-Target") {
			case "Logs_20140328.StartQuery":
				Expect(in).To(Equal(map[string]any{"logGroupName": "grp", "queryString": "fields @message", "startTime": float64(100), "endTime": float64(200), "limit": float64(5)}))
				_, _ = w.Write([]byte(`{"queryId":"q-1"}`))
			case "Logs_20140328.GetQueryResults":
				Expect(in).To(Equal(map[string]any{"queryId": "q-1"}))
				_, _ = w.Write([]byte(`{"status":"Complete","results":[[{"field":"@message","value":"hi"}]],"statistics":{"recordsMatched":3}}`))
			default:
				Fail("unexpected target " + r.Header.Get("X-Amz-Target"))
			}
		})
		logs := session.Logs()
		id, err := logs.StartQuery(ctx, cloudaws.StartQueryInput{LogGroupName: "grp", QueryString: "fields @message", StartTime: 100, EndTime: 200, Limit: 5})
		Expect(err).ToNot(HaveOccurred())
		results, err := logs.GetQueryResults(ctx, id)
		Expect(err).ToNot(HaveOccurred())
		Expect(*results).To(Equal(cloudaws.QueryResults{
			Status:     cloudaws.QueryStatusComplete,
			Results:    [][]cloudaws.QueryResultField{{{Field: "@message", Value: "hi"}}},
			Statistics: cloudaws.QueryStatistics{RecordsMatched: 3},
		}))
	})
})

var _ = Describe("CloudWatch metrics", func() {
	It("encodes GetMetricData queries as query-protocol members and decodes datapoints", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "monitoring")
			Expect(readForm(r)).To(Equal(url.Values{
				"Action": {"GetMetricData"}, "Version": {"2010-08-01"},
				"StartTime": {"2024-05-01T10:00:00Z"}, "EndTime": {"2024-05-01T11:00:00Z"},
				"ScanBy": {"TimestampAscending"}, "NextToken": {"page-2"},
				"MetricDataQueries.member.1.Id":                                          {"m0"},
				"MetricDataQueries.member.1.MetricStat.Metric.Namespace":                 {"AWS/EC2"},
				"MetricDataQueries.member.1.MetricStat.Metric.MetricName":                {"CPUUtilization"},
				"MetricDataQueries.member.1.MetricStat.Metric.Dimensions.member.1.Name":  {"InstanceId"},
				"MetricDataQueries.member.1.MetricStat.Metric.Dimensions.member.1.Value": {"i-1"},
				"MetricDataQueries.member.1.MetricStat.Period":                           {"60"},
				"MetricDataQueries.member.1.MetricStat.Stat":                             {"Average"},
				"MetricDataQueries.member.1.MetricStat.Unit":                             {"Percent"},
				"MetricDataQueries.member.2.Id":                                          {"e1"},
				"MetricDataQueries.member.2.Expression":                                  {"SELECT 1"},
				"MetricDataQueries.member.2.Period":                                      {"60"},
			}))
			_, _ = w.Write([]byte(`<GetMetricDataResponse><GetMetricDataResult><MetricDataResults><member><Id>m0</Id><Label>cpu</Label><StatusCode>PartialData</StatusCode>` +
				`<Timestamps><member>2024-05-01T10:01:00Z</member></Timestamps><Values><member>12.5</member></Values><Messages><member><Code>c</Code><Value>v</Value></member></Messages></member></MetricDataResults>` +
				`<NextToken>page-3</NextToken></GetMetricDataResult></GetMetricDataResponse>`))
		})
		out, err := session.CloudWatch().GetMetricData(ctx, cloudaws.GetMetricDataInput{
			StartTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2024, 5, 1, 11, 0, 0, 0, time.UTC),
			ScanBy: cloudaws.ScanByTimestampAscending, NextToken: "page-2",
			Queries: []cloudaws.MetricDataQuery{
				{ID: "m0", MetricStat: &cloudaws.MetricStat{Metric: cloudaws.Metric{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []cloudaws.Dimension{{Name: "InstanceId", Value: "i-1"}}}, Period: 60, Stat: "Average", Unit: "Percent"}},
				{ID: "e1", Expression: "SELECT 1", Period: 60},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(*out).To(Equal(cloudaws.GetMetricDataOutput{
			NextToken: "page-3",
			Results: []cloudaws.MetricDataResult{{
				ID: "m0", Label: "cpu", StatusCode: cloudaws.StatusCodePartialData,
				Timestamps: []time.Time{time.Date(2024, 5, 1, 10, 1, 0, 0, time.UTC)}, Values: []float64{12.5},
				Messages: []cloudaws.MetricMessage{{Code: "c", Value: "v"}},
			}},
		}))
	})

	It("lists metrics with dimension filters and a continuation token", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(readForm(r)).To(Equal(url.Values{
				"Action": {"ListMetrics"}, "Version": {"2010-08-01"}, "Namespace": {"AWS/EC2"}, "MetricName": {"CPUUtilization"},
				"Dimensions.member.1.Name": {"InstanceId"}, "Dimensions.member.1.Value": {"i-1"},
			}))
			_, _ = w.Write([]byte(`<ListMetricsResponse><ListMetricsResult><Metrics><member><Namespace>AWS/EC2</Namespace><MetricName>CPUUtilization</MetricName>` +
				`<Dimensions><member><Name>InstanceId</Name><Value>i-1</Value></member></Dimensions></member></Metrics><NextToken>n</NextToken></ListMetricsResult></ListMetricsResponse>`))
		})
		out, err := session.CloudWatch().ListMetrics(ctx, cloudaws.ListMetricsInput{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []cloudaws.Dimension{{Name: "InstanceId", Value: "i-1"}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(*out).To(Equal(cloudaws.ListMetricsOutput{NextToken: "n", Metrics: []cloudaws.Metric{{Namespace: "AWS/EC2", MetricName: "CPUUtilization", Dimensions: []cloudaws.Dimension{{Name: "InstanceId", Value: "i-1"}}}}}))
	})
})

var _ = Describe("SQS", func() {
	It("receives with all attributes, deletes and changes visibility over JSON 1.0", func(ctx SpecContext) {
		var calls []string
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "sqs")
			Expect(r.Header.Get("Content-Type")).To(Equal("application/x-amz-json-1.0"))
			var in map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&in)).To(Succeed())
			target := r.Header.Get("X-Amz-Target")
			calls = append(calls, target)
			switch target {
			case "AmazonSQS.ReceiveMessage":
				Expect(in).To(Equal(map[string]any{
					"QueueUrl": "http://q/1", "MaxNumberOfMessages": float64(10), "WaitTimeSeconds": float64(5),
					"MessageAttributeNames": []any{"All"}, "MessageSystemAttributeNames": []any{"All"},
				}))
				_, _ = w.Write([]byte(`{"Messages":[{"MessageId":"m1","ReceiptHandle":"rh","Body":"{}","Attributes":{"SentTimestamp":"1"},"MessageAttributes":{"k":{"DataType":"String","StringValue":"v"}}}]}`))
			case "AmazonSQS.DeleteMessage":
				Expect(in).To(Equal(map[string]any{"QueueUrl": "http://q/1", "ReceiptHandle": "rh"}))
			case "AmazonSQS.ChangeMessageVisibility":
				Expect(in).To(Equal(map[string]any{"QueueUrl": "http://q/1", "ReceiptHandle": "rh", "VisibilityTimeout": float64(0)}))
			}
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		})
		sqs := session.SQS()
		messages, err := sqs.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{QueueURL: "http://q/1", MaxNumberOfMessages: 10, WaitTimeSeconds: 5})
		Expect(err).ToNot(HaveOccurred())
		Expect(messages).To(Equal([]cloudaws.Message{{
			MessageID: "m1", ReceiptHandle: "rh", Body: "{}",
			Attributes:        map[string]string{"SentTimestamp": "1"},
			MessageAttributes: map[string]cloudaws.MessageAttribute{"k": {DataType: "String", StringValue: "v"}},
		}}))
		Expect(sqs.DeleteMessage(ctx, "http://q/1", "rh")).To(Succeed())
		Expect(sqs.ChangeMessageVisibility(ctx, "http://q/1", "rh", 0)).To(Succeed())
		Expect(calls).To(Equal([]string{"AmazonSQS.ReceiveMessage", "AmazonSQS.DeleteMessage", "AmazonSQS.ChangeMessageVisibility"}))
	})
})

var _ = Describe("EKS token", func() {
	It("is the k8s-aws-v1 prefix over a presigned STS URL bound to the cluster header", func() {
		token, err := cloudaws.EKSToken(awsCreds(), "prod", time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC))
		Expect(err).ToNot(HaveOccurred())
		Expect(token).To(HavePrefix("k8s-aws-v1."))
		decoded, err := base64URLDecode(token[len("k8s-aws-v1."):])
		Expect(err).ToNot(HaveOccurred())
		u, err := url.Parse(decoded)
		Expect(err).ToNot(HaveOccurred())
		Expect(u.Host).To(Equal("sts.amazonaws.com"))
		q := u.Query()
		Expect(q.Get("Action")).To(Equal("GetCallerIdentity"))
		Expect(q.Get("X-Amz-SignedHeaders")).To(Equal("host;x-k8s-aws-id"))
		Expect(q.Get("X-Amz-Expires")).To(Equal("86400"))
		Expect(q.Get("X-Amz-Credential")).To(Equal(testAccessKey + "/20240501/us-east-1/sts/aws4_request"))
		Expect(q.Get("X-Amz-Security-Token")).To(Equal("example-token"))
		Expect(q.Get("X-Amz-Signature")).To(HaveLen(64))
	})
})
