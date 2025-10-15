package fs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// GCSFileInfo wraps GCS object attributes to implement os.FileInfo and FileInfo
type GCSFileInfo struct {
	Object *gcs.ObjectAttrs
}

func (GCSFileInfo) IsDir() bool {
	return false
}

func (obj GCSFileInfo) ModTime() time.Time {
	return obj.Object.Updated
}

func (obj GCSFileInfo) Mode() fs.FileMode {
	return fs.FileMode(0644)
}

func (obj GCSFileInfo) Name() string {
	return obj.Object.Name
}

func (obj GCSFileInfo) Size() int64 {
	return obj.Object.Size
}

func (obj GCSFileInfo) Sys() interface{} {
	return obj.Object
}

func (obj GCSFileInfo) FullPath() string {
	return obj.Object.Name
}

// gcsFS implements FilesystemRW for Google Cloud Storage
type gcsFS struct {
	*gcs.Client
	Bucket string
}

func NewGCSFS(ctx context.Context, bucket string, client *gcs.Client) *gcsFS {
	fs := gcsFS{
		Bucket: strings.TrimPrefix(bucket, "gcs://"),
		Client: client,
	}

	return &fs
}

func (t *gcsFS) Close() error {
	return t.Client.Close()
}

func (t *gcsFS) ReadDir(name string) ([]FileInfo, error) {
	bucket := t.Client.Bucket(t.Bucket)
	objs := bucket.Objects(context.TODO(), &gcs.Query{Prefix: name})

	var output []FileInfo
	for {
		obj, err := objs.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}

			return nil, err
		}

		if obj == nil {
			break
		}

		file := GCSFileInfo{Object: obj}
		output = append(output, file)
	}

	return output, nil
}

func (t *gcsFS) Stat(path string) (os.FileInfo, error) {
	obj := t.Client.Bucket(t.Bucket).Object(path)
	attrs, err := obj.Attrs(context.TODO())
	if err != nil {
		return nil, err
	}

	fileInfo := &GCSFileInfo{
		Object: attrs,
	}

	return fileInfo, nil
}

func (t *gcsFS) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	obj := t.Client.Bucket(t.Bucket).Object(path)

	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, err
	}

	return reader, nil
}

func (t *gcsFS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	obj := t.Client.Bucket(t.Bucket).Object(path)

	content, err := io.ReadAll(data)
	if err != nil {
		return nil, err
	}

	writer := obj.NewWriter(ctx)
	if _, err := writer.Write(content); err != nil {
		return nil, err
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	return t.Stat(path)
}
