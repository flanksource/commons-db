package fs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cloud/gcp"
)

// GCSFileInfo wraps GCS object attributes to implement os.FileInfo and FileInfo
type GCSFileInfo struct {
	Object *gcp.Object
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
	client *gcp.StorageClient
	Bucket string
}

func NewGCSFS(ctx context.Context, bucket string, client *gcp.StorageClient) *gcsFS {
	return &gcsFS{
		Bucket: strings.TrimPrefix(bucket, "gcs://"),
		client: client,
	}
}

// Close is a no-op: the REST client holds no resources that outlive a request.
func (t *gcsFS) Close() error {
	return nil
}

// ReadDir lists every object whose name starts with name.
func (t *gcsFS) ReadDir(name string) ([]FileInfo, error) {
	objects, err := t.client.ListObjects(context.TODO(), t.Bucket, name)
	if err != nil {
		return nil, err
	}

	var output []FileInfo
	for _, obj := range objects {
		output = append(output, GCSFileInfo{Object: obj})
	}

	return output, nil
}

func (t *gcsFS) Stat(path string) (os.FileInfo, error) {
	attrs, err := t.client.GetObject(context.TODO(), t.Bucket, path)
	if err != nil {
		return nil, err
	}

	return &GCSFileInfo{Object: attrs}, nil
}

func (t *gcsFS) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	return t.client.OpenObject(ctx, t.Bucket, path)
}

// Write streams data to the object, holding at most one upload chunk in memory.
func (t *gcsFS) Write(ctx context.Context, path string, data io.Reader) (os.FileInfo, error) {
	object, err := t.client.WriteObject(ctx, gcp.WriteObjectOptions{Bucket: t.Bucket, Name: path, Body: data})
	if err != nil {
		return nil, err
	}

	return &GCSFileInfo{Object: object}, nil
}

// Delete removes the object at path. A missing object is not an error.
func (t *gcsFS) Delete(ctx context.Context, path string) error {
	err := t.client.DeleteObject(ctx, t.Bucket, path)
	if gcp.IsNotFound(err) {
		return nil
	}
	return err
}
