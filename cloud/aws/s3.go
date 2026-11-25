package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// S3Options selects how bucket names are addressed.
type S3Options struct {
	// PathStyle sends http://host/BUCKET/KEY instead of http://BUCKET.host/KEY,
	// where host is the session Endpoint or the regional S3 endpoint. Bucket names
	// containing dots always use path style.
	PathStyle bool
}

// S3 is the Simple Storage Service client (REST-XML).
type S3 struct {
	svc       *service
	session   *Session
	pathStyle bool
}

// S3 returns the S3 client.
func (s *Session) S3(options S3Options) *S3 {
	return &S3{
		svc:       s.newService("s3", "s3", "s3"),
		session:   s,
		pathStyle: options.PathStyle,
	}
}

// S3ObjectRef names one object.
type S3ObjectRef struct{ Bucket, Key string }

// S3Object is the listing metadata of one object.
type S3Object struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
}

// S3Bucket is one bucket in a ListBuckets response.
type S3Bucket struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

// S3ObjectHead is the metadata HeadObject returns.
type S3ObjectHead struct {
	Size         int64
	LastModified time.Time
	ETag         string
}

// S3ObjectBody is a streamed object; the caller closes Body.
type S3ObjectBody struct {
	S3ObjectHead
	Body io.ReadCloser
}

// ListObjectsInput selects one page of a ListObjectsV2 listing.
type ListObjectsInput struct {
	Bucket            string
	Prefix            string
	ContinuationToken string
	// MaxKeys caps the page size when positive.
	MaxKeys int
}

// ListObjectsOutput is one page of a ListObjectsV2 listing.
type ListObjectsOutput struct {
	Objects               []S3Object `xml:"Contents"`
	KeyCount              int        `xml:"KeyCount"`
	NextContinuationToken string     `xml:"NextContinuationToken"`
}

// target is the base URL and escaped path addressing bucket/key.
func (c *S3) target(bucket, key string) (baseURL, path string) {
	if c.pathStyle || strings.Contains(bucket, ".") {
		if key == "" {
			return c.svc.baseURL, escapePath(bucket)
		}
		return c.svc.baseURL, escapePath(bucket, key)
	}
	return c.session.virtualHostBase(bucket), escapePath(key)
}

// virtualHostBase is the bucket as a subdomain of the endpoint override, or of
// the regional S3 endpoint without one.
func (s *Session) virtualHostBase(bucket string) string {
	if s.endpoint != "" {
		return strings.Replace(s.endpoint, "://", "://"+bucket+".", 1)
	}
	return fmt.Sprintf("https://%s.s3.%s.%s", bucket, s.Region(), s.dnsSuffix())
}

func (c *S3) objectCall(operation, method string, ref S3ObjectRef) call {
	base, path := c.target(ref.Bucket, ref.Key)
	return call{Operation: operation, Method: method, BaseURL: base, Path: path}
}

// ListBuckets lists the account's buckets.
func (c *S3) ListBuckets(ctx context.Context) ([]S3Bucket, error) {
	var out struct {
		Buckets []S3Bucket `xml:"Buckets>Bucket"`
	}
	if err := c.svc.do(ctx, call{Operation: "ListBuckets", Method: http.MethodGet, Path: "/"}, &out, decodeXML); err != nil {
		return nil, err
	}
	return out.Buckets, nil
}

// HeadBucket fails unless the bucket exists and is accessible.
func (c *S3) HeadBucket(ctx context.Context, bucket string) error {
	base, path := c.target(bucket, "")
	return c.svc.do(ctx, call{Operation: "HeadBucket", Method: http.MethodHead, BaseURL: base, Path: path}, nil, nil)
}

// ListObjectsV2 returns one page of objects.
func (c *S3) ListObjectsV2(ctx context.Context, in ListObjectsInput) (*ListObjectsOutput, error) {
	base, path := c.target(in.Bucket, "")
	query := url.Values{"list-type": {"2"}, "prefix": {in.Prefix}}
	if in.ContinuationToken != "" {
		query.Set("continuation-token", in.ContinuationToken)
	}
	if in.MaxKeys > 0 {
		query.Set("max-keys", strconv.Itoa(in.MaxKeys))
	}
	var out ListObjectsOutput
	if err := c.svc.do(ctx, call{Operation: "ListObjectsV2", Method: http.MethodGet, BaseURL: base, Path: path, Query: query}, &out, decodeXML); err != nil {
		return nil, err
	}
	return &out, nil
}

// HeadObject returns an object's size, modification time and ETag.
func (c *S3) HeadObject(ctx context.Context, ref S3ObjectRef) (*S3ObjectHead, error) {
	resp, err := c.svc.send(ctx, c.objectCall("HeadObject", http.MethodHead, ref))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return headFromResponse(resp.Response)
}

// GetObject opens an object for streaming. The caller closes Body.
func (c *S3) GetObject(ctx context.Context, ref S3ObjectRef) (*S3ObjectBody, error) {
	resp, err := c.svc.send(ctx, c.objectCall("GetObject", http.MethodGet, ref))
	if err != nil {
		return nil, err
	}
	head, err := headFromResponse(resp.Response)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return &S3ObjectBody{S3ObjectHead: *head, Body: resp.Body}, nil
}

// PutObject uploads body. The SigV4 signer hashes the payload, so the body is
// read fully before sending.
func (c *S3) PutObject(ctx context.Context, ref S3ObjectRef, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("aws s3 PutObject: reading body for %s/%s: %w", ref.Bucket, ref.Key, err)
	}
	op := c.objectCall("PutObject", http.MethodPut, ref)
	op.Body = data
	return c.svc.do(ctx, op, nil, nil)
}

// DeleteObject removes an object. S3 answers 204 for a missing key.
func (c *S3) DeleteObject(ctx context.Context, ref S3ObjectRef) error {
	return c.svc.do(ctx, c.objectCall("DeleteObject", http.MethodDelete, ref), nil, nil)
}

func headFromResponse(resp *http.Response) (*S3ObjectHead, error) {
	head := &S3ObjectHead{ETag: resp.Header.Get("ETag"), Size: resp.ContentLength}
	if raw := resp.Header.Get("Last-Modified"); raw != "" {
		t, err := http.ParseTime(raw)
		if err != nil {
			return nil, fmt.Errorf("aws s3: invalid Last-Modified %q: %w", raw, err)
		}
		head.LastModified = t
	}
	return head, nil
}
