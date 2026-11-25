package aws_test

import (
	"errors"
	"io/fs"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
)

var _ = Describe("API errors", func() {
	It("parses the AWS JSON protocol error shape", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-amzn-RequestId", "req-json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.kms#NotFoundException","message":"key gone"}`))
		})
		_, err := session.KMS().Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: "alias/x", Plaintext: []byte("p")})

		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(*apiErr).To(Equal(cloudaws.APIError{Service: "kms", Operation: "Encrypt", StatusCode: 400, Code: "NotFoundException", Message: "key gone", RequestID: "req-json"}))
		Expect(cloudaws.IsNotFound(err)).To(BeTrue())
	})

	It("parses the S3 XML error shape and treats NoSuchKey as not found", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>missing</Message><RequestId>req-xml</RequestId></Error>`))
		})
		_, err := session.S3(cloudaws.S3Options{PathStyle: true}).GetObject(ctx, cloudaws.S3ObjectRef{Bucket: "b", Key: "k"})

		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(*apiErr).To(Equal(cloudaws.APIError{Service: "s3", Operation: "GetObject", StatusCode: 404, Code: "NoSuchKey", Message: "missing", RequestID: "req-xml"}))
		Expect(cloudaws.IsNotFound(err)).To(BeTrue())
		Expect(errors.Is(err, fs.ErrNotExist)).To(BeTrue())
	})

	It("parses the query protocol ErrorResponse envelope and does not call a 403 not found", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>nope</Message></Error><RequestId>req-q</RequestId></ErrorResponse>`))
		})
		_, err := session.STS().GetCallerIdentity(ctx)

		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(*apiErr).To(Equal(cloudaws.APIError{Service: "sts", Operation: "GetCallerIdentity", StatusCode: 403, Code: "AccessDenied", Message: "nope", RequestID: "req-q"}))
		Expect(cloudaws.IsNotFound(err)).To(BeFalse())
	})

	It("keeps the status code when the body is not parseable", func(ctx SpecContext) {
		session, _ := fakeAWS(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
		_, err := session.S3(cloudaws.S3Options{PathStyle: true}).HeadObject(ctx, cloudaws.S3ObjectRef{Bucket: "b", Key: "k"})

		var apiErr *cloudaws.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(apiErr.StatusCode).To(Equal(404))
		Expect(cloudaws.IsNotFound(err)).To(BeTrue())
	})
})
