//go:build !fast

package fs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/flanksource/commons/utils"
	"github.com/samber/lo"
)

const s3ListObjectMaxKeys = 1000

// S3FileInfo wraps S3 object metadata to implement os.FileInfo and FileInfo
type S3FileInfo struct {
	Object s3Types.Object
}

func (obj S3FileInfo) Name() string {
	return *obj.Object.Key
}

func (obj S3FileInfo) Size() int64 {
	return utils.Deref(obj.Object.Size)
}

func (obj S3FileInfo) Mode() fs.FileMode {
	return fs.FileMode(0644)
}

func (obj S3FileInfo) ModTime() time.Time {
	return lo.FromPtr(obj.Object.LastModified)
}

func (obj S3FileInfo) FullPath() string {
	return *obj.Object.Key
}

func (obj S3FileInfo) IsDir() bool {
	return strings.HasSuffix(obj.Name(), "/")
}

func (obj S3FileInfo) Sys() interface{} {
	return obj.Object
}

// s3FS implements
// - FilesystemRW for S3
// - fs.FS for glob support
type s3FS struct {
	// maxObjects limits the total number of objects ReadDir can return.
	maxObjects int

	Client *s3.Client
	Bucket string
}

func NewS3FS(ctx context.Context, bucket string, cfg aws.Config) *s3FS {
	client := &s3FS{
		maxObjects: 50 * 10_000,
		Client:     s3.NewFromConfig(cfg),
		Bucket:     strings.TrimPrefix(bucket, "s3://"),
	}

	return client
}

func (t *s3FS) SetMaxListItems(max int) {
	t.maxObjects = max
}

func (t *s3FS) Close() error {
	return nil // NOOP
}

func (t *s3FS) ReadDir(pattern string) ([]FileInfo, error) {
	prefix, _ := doublestar.SplitPattern(pattern)
	if prefix == "." {
		prefix = ""
	}

	req := &s3.ListObjectsV2Input{
		Bucket: aws.String(t.Bucket),
		Prefix: aws.String(prefix),
	}

	if t.maxObjects < s3ListObjectMaxKeys {
		req.MaxKeys = lo.ToPtr(int32(t.maxObjects))
	}

	var output []FileInfo
	var numObjectsFetched int
	for {
		resp, err := t.Client.ListObjectsV2(context.TODO(), req)
		if err != nil {
			return nil, err
		}

		for _, obj := range resp.Contents {
			if pattern != "" {
				if matched, err := doublestar.Match(pattern, *obj.Key); err != nil {
					return nil, err
				} else if !matched {
					continue
				}
			}

			fileInfo := &S3FileInfo{Object: obj}
			output = append(output, fileInfo)
		}

		if resp.NextContinuationToken == nil {
			break
		}

		numObjectsFetched += int(*resp.KeyCount)
		if numObjectsFetched >= t.maxObjects {
			break
		}

		req.ContinuationToken = resp.NextContinuationToken
	}

	return output, nil
}

func (t *s3FS) Stat(path string) (fs.FileInfo, error) {
	headObject, err := t.Client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(t.Bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}

	fileInfo := &S3FileInfo{
		Object: s3Types.Object{
			Key:          utils.Ptr(filepath.Base(path)),
			Size:         headObject.ContentLength,
			LastModified: headObject.LastModified,
			ETag:         headObject.ETag,
		},
	}

	return fileInfo, nil
}

func (t *s3FS) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	results, err := t.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(t.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	return results.Body, nil
}

func (t *s3FS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	_, err := t.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.Bucket),
		Key:    aws.String(path),
		Body:   data,
	})

	if err != nil {
		return nil, err
	}

	return t.Stat(path)
}
