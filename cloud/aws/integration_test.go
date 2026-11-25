package aws_test

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/cloud/aws/awstest"
	"github.com/flanksource/commons-db/connection"
	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/cloudwatch"
	"github.com/flanksource/commons-db/types"
)

// These specs run against the ministack emulator, started in Docker (possibly
// on a remote host) and addressed through the endpoint it reports.
var _ = Describe("ministack integration", Label("integration"), Ordered, func() {
	var (
		emulator *awstest.Emulator
		session  *cloudaws.Session
		suffix   = fmt.Sprintf("%d", time.Now().UnixNano())
	)

	BeforeAll(func(ctx SpecContext) {
		var err error
		emulator, err = awstest.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) { Expect(emulator.Stop(ctx)).To(Succeed()) }, NodeTimeout(2*time.Minute))

		session, err = cloudaws.NewSession(emulator.SessionOptions())
		Expect(err).ToNot(HaveOccurred())
	}, NodeTimeout(10*time.Minute))

	awsConnection := func() connection.AWSConnection {
		return connection.AWSConnection{
			Endpoint: emulator.Endpoint, Region: emulator.Region,
			AccessKey: types.EnvVar{ValueStatic: emulator.AccessKey}, SecretKey: types.EnvVar{ValueStatic: emulator.SecretKey},
		}
	}

	It("STS GetCallerIdentity reports the account", func(ctx SpecContext) {
		identity, err := session.STS().GetCallerIdentity(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(identity.Account).To(MatchRegexp(`^\d{12}$`))
		Expect(identity.ARN).To(HavePrefix("arn:aws:"))
	})

	It("S3 write, stat, list, read and delete through the fs filesystem", func(ctx SpecContext) {
		bucket := "commons-db-" + suffix
		Expect(session.S3(cloudaws.S3Options{PathStyle: true}).CreateBucket(ctx, bucket)).To(Succeed())
		conn := connection.S3Connection{AWSConnection: awsConnection(), Bucket: bucket, UsePathStyle: true}
		filesystem, err := conn.Filesystem(dbctx.New())
		Expect(err).ToNot(HaveOccurred())
		key := "dir/a b+c.txt"

		info, err := filesystem.Write(ctx, key, strings.NewReader("hello world"))
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Size()).To(Equal(int64(11)))

		listed, err := filesystem.ReadDir("dir/*")
		Expect(err).ToNot(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].FullPath()).To(Equal(key))

		reader, err := filesystem.Read(ctx, key)
		Expect(err).ToNot(HaveOccurred())
		content, err := io.ReadAll(reader)
		Expect(err).ToNot(HaveOccurred())
		Expect(reader.Close()).To(Succeed())
		Expect(string(content)).To(Equal("hello world"))

		Expect(filesystem.Delete(ctx, key)).To(Succeed())
		_, err = filesystem.Stat(key)
		Expect(cloudaws.IsNotFound(err)).To(BeTrue())
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())
		Expect(filesystem.Delete(ctx, key)).To(Succeed())
	})

	DescribeTable("S3 put, get and list round-trip keys with reserved characters",
		func(ctx SpecContext, key string) {
			s3 := session.S3(cloudaws.S3Options{PathStyle: true})
			bucket := fmt.Sprintf("keys-%s-%d", suffix, GinkgoParallelProcess())
			if err := s3.CreateBucket(ctx, bucket); err != nil {
				Expect(err).To(MatchError(ContainSubstring("BucketAlready")))
			}
			ref := cloudaws.S3ObjectRef{Bucket: bucket, Key: key}

			Expect(s3.PutObject(ctx, ref, strings.NewReader("payload"))).To(Succeed())
			object, err := s3.GetObject(ctx, ref)
			Expect(err).ToNot(HaveOccurred())
			content, err := io.ReadAll(object.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(object.Body.Close()).To(Succeed())
			Expect(string(content)).To(Equal("payload"))

			listed, err := s3.ListObjectsV2(ctx, cloudaws.ListObjectsInput{Bucket: bucket, Prefix: key})
			Expect(err).ToNot(HaveOccurred())
			Expect(listed.Objects).To(HaveLen(1))
			Expect(listed.Objects[0].Key).To(Equal(key))
			Expect(s3.DeleteObject(ctx, ref)).To(Succeed())
		},
		Entry("reserved punctuation", "logs/dt=2024-01-01/a:b+c.json"),
		Entry("space and non-ASCII", "space key/ü.txt"),
		Entry("unreserved only", "a~b_c-d.e"),
	)

	It("KMS encrypt then decrypt round-trips through the KMS client", func(ctx SpecContext) {
		kms := session.KMS()
		keyID, err := kms.CreateKey(ctx)
		Expect(err).ToNot(HaveOccurred())

		ciphertext, err := kms.Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: keyID, Plaintext: []byte("top secret")})
		Expect(err).ToNot(HaveOccurred())
		Expect(ciphertext).ToNot(Equal([]byte("top secret")))
		plaintext, err := kms.Decrypt(ctx, cloudaws.KMSDecryptInput{CiphertextBlob: ciphertext})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(plaintext)).To(Equal("top secret"))
	})

	It("CloudWatch Logs StartQuery then GetQueryResults returns the ingested event", func(ctx SpecContext) {
		logs := session.Logs()
		group, stream := "/commons-db/"+suffix, "stream-a"
		Expect(logs.CreateLogGroup(ctx, group)).To(Succeed())
		Expect(logs.CreateLogStream(ctx, group, stream)).To(Succeed())
		// The query end is whole seconds, so an event stamped "now" can fall just after it.
		eventTime := time.Now().Add(-time.Minute).UnixMilli()
		Expect(logs.PutLogEvents(ctx, group, stream, []cloudaws.LogEvent{{Timestamp: eventTime, Message: "integration hello"}})).To(Succeed())

		request := cloudwatch.Request{LogGroup: group, Query: "fields @timestamp, @message | sort @timestamp desc"}
		request.Start = "now-1h"
		searcher := cloudwatch.New(logs, nil)
		searcher.PollInterval = 200 * time.Millisecond

		result, err := searcher.Search(ctx, request)
		Expect(err).ToNot(HaveOccurred())
		messages := []string{}
		for _, line := range result.Logs {
			messages = append(messages, line.Message)
		}
		Expect(messages).To(ContainElement("integration hello"))
	})

	It("CloudWatch ListMetrics and GetMetricData read a published datapoint", func(ctx SpecContext) {
		cw := session.CloudWatch()
		namespace, metric := "CommonsDB/"+suffix, "Queue"
		dimensions := []cloudaws.Dimension{{Name: "Env", Value: "test"}}
		now := time.Now().UTC().Truncate(time.Minute)
		Expect(cw.PutMetricData(ctx, namespace, []cloudaws.MetricDatum{{MetricName: metric, Dimensions: dimensions, Timestamp: now, Value: 7, Unit: "Count"}})).To(Succeed())

		listed, err := cw.ListMetrics(ctx, cloudaws.ListMetricsInput{Namespace: namespace})
		Expect(err).ToNot(HaveOccurred())
		Expect(listed.Metrics).To(ContainElement(cloudaws.Metric{Namespace: namespace, MetricName: metric, Dimensions: dimensions}))

		out, err := cw.GetMetricData(ctx, cloudaws.GetMetricDataInput{
			StartTime: now.Add(-time.Hour), EndTime: now.Add(time.Hour), ScanBy: cloudaws.ScanByTimestampAscending,
			Queries: []cloudaws.MetricDataQuery{{ID: "m0", MetricStat: &cloudaws.MetricStat{
				Metric: cloudaws.Metric{Namespace: namespace, MetricName: metric, Dimensions: dimensions}, Period: 60, Stat: "Sum",
			}}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(out.Results).To(HaveLen(1))
		Expect(out.Results[0].Values).To(ConsistOf(7.0))
	})

	It("SQS send, receive, change visibility and delete", func(ctx SpecContext) {
		sqs := session.SQS()
		queueURL, err := sqs.CreateQueue(ctx, "commons-db-"+suffix)
		Expect(err).ToNot(HaveOccurred())
		_, err = sqs.SendMessage(ctx, queueURL, `{"hello":"queue"}`)
		Expect(err).ToNot(HaveOccurred())

		messages, err := sqs.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{QueueURL: queueURL, MaxNumberOfMessages: 1, WaitTimeSeconds: 2})
		Expect(err).ToNot(HaveOccurred())
		Expect(messages).To(HaveLen(1))
		Expect(messages[0].Body).To(Equal(`{"hello":"queue"}`))
		Expect(sqs.ChangeMessageVisibility(ctx, queueURL, messages[0].ReceiptHandle, 0)).To(Succeed())

		again, err := sqs.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{QueueURL: queueURL, MaxNumberOfMessages: 1, WaitTimeSeconds: 2})
		Expect(err).ToNot(HaveOccurred())
		Expect(again).To(HaveLen(1))
		Expect(sqs.DeleteMessage(ctx, queueURL, again[0].ReceiptHandle)).To(Succeed())
		empty, err := sqs.ReceiveMessage(ctx, cloudaws.ReceiveMessageInput{QueueURL: queueURL, MaxNumberOfMessages: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(empty).To(BeEmpty())
	})
})
