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

	"github.com/bmatcuk/doublestar/v4"
	cloudaws "github.com/flanksource/commons-db/cloud/aws"
)

const s3ListObjectMaxKeys = 1000

// S3FileInfo wraps S3 object metadata to implement os.FileInfo and FileInfo
type S3FileInfo struct {
	Object cloudaws.S3Object
}

func (obj S3FileInfo) Name() string {
	return obj.Object.Key
}

func (obj S3FileInfo) Size() int64 {
	return obj.Object.Size
}

func (obj S3FileInfo) Mode() fs.FileMode {
	return fs.FileMode(0644)
}

func (obj S3FileInfo) ModTime() time.Time {
	return obj.Object.LastModified
}

func (obj S3FileInfo) FullPath() string {
	return obj.Object.Key
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

	Client *cloudaws.S3
	Bucket string
}

var _ FilesystemRW = (*s3FS)(nil)

// S3FSOptions configures NewS3FS.
type S3FSOptions struct {
	// Bucket is the bucket name, optionally prefixed with s3://.
	Bucket  string
	Session *cloudaws.Session
	// S3 selects addressing, e.g. path-style for emulators.
	S3 cloudaws.S3Options
}

// NewS3FS returns a filesystem over the bucket.
func NewS3FS(options S3FSOptions) *s3FS {
	return &s3FS{
		maxObjects: 50 * 10_000,
		Client:     options.Session.S3(options.S3),
		Bucket:     strings.TrimPrefix(options.Bucket, "s3://"),
	}
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

	req := cloudaws.ListObjectsInput{Bucket: t.Bucket, Prefix: prefix}
	if t.maxObjects < s3ListObjectMaxKeys {
		req.MaxKeys = t.maxObjects
	}

	var output []FileInfo
	var numObjectsFetched int
	for {
		resp, err := t.Client.ListObjectsV2(context.TODO(), req)
		if err != nil {
			return nil, err
		}

		for _, obj := range resp.Objects {
			if pattern != "" {
				if matched, err := doublestar.Match(pattern, obj.Key); err != nil {
					return nil, err
				} else if !matched {
					continue
				}
			}

			output = append(output, &S3FileInfo{Object: obj})
		}

		if resp.NextContinuationToken == "" {
			break
		}

		numObjectsFetched += resp.KeyCount
		if numObjectsFetched >= t.maxObjects {
			break
		}

		req.ContinuationToken = resp.NextContinuationToken
	}

	return output, nil
}

func (t *s3FS) Stat(path string) (fs.FileInfo, error) {
	head, err := t.Client.HeadObject(context.TODO(), cloudaws.S3ObjectRef{Bucket: t.Bucket, Key: path})
	if err != nil {
		return nil, err
	}

	return &S3FileInfo{
		Object: cloudaws.S3Object{
			Key:          filepath.Base(path),
			Size:         head.Size,
			LastModified: head.LastModified,
			ETag:         head.ETag,
		},
	}, nil
}

func (t *s3FS) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := t.Client.GetObject(ctx, cloudaws.S3ObjectRef{Bucket: t.Bucket, Key: key})
	if err != nil {
		return nil, err
	}

	return object.Body, nil
}

func (t *s3FS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	if err := t.Client.PutObject(ctx, cloudaws.S3ObjectRef{Bucket: t.Bucket, Key: path}, data); err != nil {
		return nil, err
	}

	return t.Stat(path)
}

// Delete removes the object at key. A missing object is not an error.
func (t *s3FS) Delete(ctx context.Context, key string) error {
	return t.Client.DeleteObject(ctx, cloudaws.S3ObjectRef{Bucket: t.Bucket, Key: key})
}
