package aws_test

import (
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
)

var _ = Describe("S3", func() {
	It("pages ListObjectsV2 with path-style addressing and a signed s3 scope", func(ctx SpecContext) {
		var seen []recorded
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			expectSigned(r, "s3")
			Expect(r.Header.Get("X-Amz-Content-Sha256")).ToNot(BeEmpty())
			seen = append(seen, recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query().Encode()})
			next := ""
			if r.URL.Query().Get("continuation-token") == "" {
				next = "<NextContinuationToken>tok-2</NextContinuationToken>"
			}
			_, _ = w.Write([]byte(`<ListBucketResult><KeyCount>1</KeyCount>` + next +
				`<Contents><Key>logs/a b.txt</Key><Size>42</Size><LastModified>2024-05-01T10:00:00.000Z</LastModified><ETag>"abc"</ETag></Contents></ListBucketResult>`))
		})
		s3 := session.S3(cloudaws.S3Options{PathStyle: true})

		first, err := s3.ListObjectsV2(ctx, cloudaws.ListObjectsInput{Bucket: "bkt", Prefix: "logs/", MaxKeys: 5})
		Expect(err).ToNot(HaveOccurred())
		second, err := s3.ListObjectsV2(ctx, cloudaws.ListObjectsInput{Bucket: "bkt", Prefix: "logs/", ContinuationToken: first.NextContinuationToken})
		Expect(err).ToNot(HaveOccurred())

		Expect(first.NextContinuationToken).To(Equal("tok-2"))
		Expect(second.NextContinuationToken).To(BeEmpty())
		Expect(first.Objects).To(Equal([]cloudaws.S3Object{{Key: "logs/a b.txt", Size: 42, LastModified: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC), ETag: `"abc"`}}))
		Expect(seen).To(Equal([]recorded{
			{Method: "GET", Path: "/bkt", Query: "list-type=2&max-keys=5&prefix=logs%2F"},
			{Method: "GET", Path: "/bkt", Query: "continuation-token=tok-2&list-type=2&prefix=logs%2F"},
		}))
	})

	It("heads an object into size, modification time and etag", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.Method).To(Equal("HEAD"))
			Expect(r.URL.EscapedPath()).To(Equal("/bkt/dir/my%20file.txt"))
			w.Header().Set("Content-Length", "11")
			w.Header().Set("Last-Modified", "Wed, 01 May 2024 10:00:00 GMT")
			w.Header().Set("ETag", `"e1"`)
		})
		head, err := session.S3(cloudaws.S3Options{PathStyle: true}).HeadObject(ctx, cloudaws.S3ObjectRef{Bucket: "bkt", Key: "dir/my file.txt"})
		Expect(err).ToNot(HaveOccurred())
		Expect(*head).To(Equal(cloudaws.S3ObjectHead{Size: 11, LastModified: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC), ETag: `"e1"`}))
	})

	It("streams GetObject without buffering the response", func(ctx SpecContext) {
		release := make(chan struct{})
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("hello"))
			w.(http.Flusher).Flush()
			<-release
			_, _ = w.Write([]byte("world"))
		})
		object, err := session.S3(cloudaws.S3Options{PathStyle: true}).GetObject(ctx, cloudaws.S3ObjectRef{Bucket: "bkt", Key: "k"})
		Expect(err).ToNot(HaveOccurred())
		defer object.Body.Close()

		firstChunk := make([]byte, 5)
		_, err = io.ReadFull(object.Body, firstChunk)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(firstChunk)).To(Equal("hello"))
		close(release)
		rest, err := io.ReadAll(object.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(rest)).To(Equal("world"))
		Expect(object.Size).To(Equal(int64(10)))
	})

	It("PutObject sends the whole body with a payload hash and DeleteObject sends DELETE", func(ctx SpecContext) {
		var seen []recorded
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			body, _ := io.ReadAll(r.Body)
			if r.Method == "PUT" {
				Expect(r.Header.Get("X-Amz-Content-Sha256")).To(Equal(sha256Hex("payload")))
			}
			seen = append(seen, recorded{Method: r.Method, Path: r.URL.Path, Body: string(body)})
			w.WriteHeader(http.StatusNoContent)
		})
		s3 := session.S3(cloudaws.S3Options{PathStyle: true})
		ref := cloudaws.S3ObjectRef{Bucket: "bkt", Key: "up/k"}

		Expect(s3.PutObject(ctx, ref, strings.NewReader("payload"))).To(Succeed())
		Expect(s3.DeleteObject(ctx, ref)).To(Succeed())
		Expect(seen[0]).To(Equal(recorded{Method: "PUT", Path: "/bkt/up/k", Body: "payload"}))
		Expect(seen[1].Method).To(Equal("DELETE"))
	})

	It("lists buckets and heads a bucket", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				Expect(r.URL.Path).To(Equal("/bkt"))
				return
			}
			_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets><Bucket><Name>one</Name></Bucket><Bucket><Name>two</Name></Bucket></Buckets></ListAllMyBucketsResult>`))
		})
		s3 := session.S3(cloudaws.S3Options{PathStyle: true})
		buckets, err := s3.ListBuckets(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(buckets).To(HaveLen(2))
		Expect(buckets[1].Name).To(Equal("two"))
		Expect(s3.HeadBucket(ctx, "bkt")).To(Succeed())
	})
})
