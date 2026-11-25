package gcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	storageDefaultRoot = "https://storage.googleapis.com"
	storageAPIPath     = "/storage/v1"
	storageUploadPath  = "/upload/storage/v1"

	// StorageEmulatorHostEnv is honoured when no endpoint is configured, matching
	// the convention used by the Google Cloud Storage client libraries.
	StorageEmulatorHostEnv = "STORAGE_EMULATOR_HOST"

	// DefaultUploadChunkSize is how much of a streamed upload is held in memory
	// at once.
	DefaultUploadChunkSize = 8 << 20
	// uploadChunkAlignment is the multiple of bytes GCS requires for every
	// resumable chunk except the last.
	uploadChunkAlignment = 256 << 10
)

// StorageClient talks to the Cloud Storage JSON API.
type StorageClient struct {
	*api
	// uploadBase is the root of the upload endpoints.
	uploadBase string
}

// Object is the subset of Cloud Storage object metadata commons-db reads.
type Object struct {
	Bucket      string
	Name        string
	ContentType string
	Size        int64
	Updated     time.Time
}

type objectWire struct {
	Bucket      string      `json:"bucket"`
	Name        string      `json:"name"`
	ContentType string      `json:"contentType"`
	Size        json.Number `json:"size"`
	Updated     time.Time   `json:"updated"`
}

// UnmarshalJSON decodes the API form, where size is a string-encoded int64.
func (o *Object) UnmarshalJSON(data []byte) error {
	var wire objectWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var size int64
	if wire.Size != "" {
		var err error
		if size, err = wire.Size.Int64(); err != nil {
			return fmt.Errorf("invalid object size %q: %w", wire.Size, err)
		}
	}
	*o = Object{Bucket: wire.Bucket, Name: wire.Name, ContentType: wire.ContentType, Size: size, Updated: wire.Updated}
	return nil
}

// NewStorageClient builds a Cloud Storage client. With no Endpoint it uses
// STORAGE_EMULATOR_HOST when set, else storage.googleapis.com. An endpoint may
// be the server root or carry the /storage/v1 path.
func NewStorageClient(opts Options) (*StorageClient, error) {
	root, err := storageRoot(opts.Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := newAPI(Options{Endpoint: root, TokenSource: opts.TokenSource, SkipTLSVerify: opts.SkipTLSVerify}, root, storageAPIPath)
	if err != nil {
		return nil, err
	}
	// Streams outlive the default whole-request timeout; contexts bound them.
	client.http.Timeout(0)
	return &StorageClient{api: client, uploadBase: root + storageUploadPath}, nil
}

func storageRoot(endpoint string) (string, error) {
	if endpoint == "" {
		endpoint = os.Getenv(StorageEmulatorHostEnv)
	}
	if endpoint == "" {
		return storageDefaultRoot, nil
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	root, err := cleanRoot(endpoint)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(root, storageAPIPath), nil
}

func (s *StorageClient) bucketURL(bucket string) string {
	return s.base + "/b/" + url.PathEscape(bucket)
}

func (s *StorageClient) objectURL(bucket, name string) string {
	return s.bucketURL(bucket) + "/o/" + url.PathEscape(name)
}

// ObjectPage is one page of an object listing.
type ObjectPage struct {
	Objects       []*Object
	NextPageToken string
}

// ListObjectsPage lists one page of objects whose names start with prefix.
func (s *StorageClient) ListObjectsPage(ctx context.Context, bucket, prefix, pageToken string) (*ObjectPage, error) {
	query := url.Values{}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	var out struct {
		Items         []*Object `json:"items"`
		NextPageToken string    `json:"nextPageToken"`
	}
	err := s.do(ctx, call{Method: http.MethodGet, URL: withQuery(s.bucketURL(bucket)+"/o", query), Out: &out})
	if err != nil {
		return nil, err
	}
	return &ObjectPage{Objects: out.Items, NextPageToken: out.NextPageToken}, nil
}

// ListObjects lists every object whose name starts with prefix, following pages.
func (s *StorageClient) ListObjects(ctx context.Context, bucket, prefix string) ([]*Object, error) {
	var objects []*Object
	token := ""
	for {
		page, err := s.ListObjectsPage(ctx, bucket, prefix, token)
		if err != nil {
			return nil, err
		}
		objects = append(objects, page.Objects...)
		if page.NextPageToken == "" {
			return objects, nil
		}
		token = page.NextPageToken
	}
}

// GetObject reads an object's metadata.
func (s *StorageClient) GetObject(ctx context.Context, bucket, name string) (*Object, error) {
	var object Object
	if err := s.do(ctx, call{Method: http.MethodGet, URL: s.objectURL(bucket, name), Out: &object}); err != nil {
		return nil, err
	}
	return &object, nil
}

// OpenObject streams an object's content; the caller closes the reader.
func (s *StorageClient) OpenObject(ctx context.Context, bucket, name string) (io.ReadCloser, error) {
	resp, err := s.send(ctx, call{Method: http.MethodGet, URL: withQuery(s.objectURL(bucket, name), url.Values{"alt": {"media"}})})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DeleteObject removes an object; a missing object is an error satisfying IsNotFound.
func (s *StorageClient) DeleteObject(ctx context.Context, bucket, name string) error {
	return s.do(ctx, call{Method: http.MethodDelete, URL: s.objectURL(bucket, name)})
}

// WriteObjectOptions describes an upload.
type WriteObjectOptions struct {
	Bucket      string
	Name        string
	Body        io.Reader
	ContentType string
	// ChunkSize bounds memory use and must be a multiple of 256 KiB;
	// DefaultUploadChunkSize when zero.
	ChunkSize int
}

// WriteObject uploads Body. A body that fits one chunk is sent in a single
// request; anything longer, or of unknown length, is streamed through a
// resumable session one chunk at a time, so memory use stays at one chunk.
func (s *StorageClient) WriteObject(ctx context.Context, o WriteObjectOptions) (*Object, error) {
	chunkSize := o.ChunkSize
	if chunkSize == 0 {
		chunkSize = DefaultUploadChunkSize
	}
	if chunkSize%uploadChunkAlignment != 0 {
		return nil, fmt.Errorf("gcp: upload chunk size %d is not a multiple of %d", chunkSize, uploadChunkAlignment)
	}
	buf := make([]byte, chunkSize)
	n, err := io.ReadFull(o.Body, buf)
	switch {
	case err == nil:
		return s.uploadResumable(ctx, o, buf)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return s.uploadMedia(ctx, o, buf[:n])
	default:
		return nil, fmt.Errorf("gcp: reading upload body for %s: %w", o.Name, err)
	}
}

func (o WriteObjectOptions) contentType() string {
	if o.ContentType == "" {
		return "application/octet-stream"
	}
	return o.ContentType
}

func (s *StorageClient) uploadURL(o WriteObjectOptions, uploadType string) string {
	query := url.Values{"uploadType": {uploadType}, "name": {o.Name}}
	return withQuery(s.uploadBase+"/b/"+url.PathEscape(o.Bucket)+"/o", query)
}

func (s *StorageClient) uploadMedia(ctx context.Context, o WriteObjectOptions, data []byte) (*Object, error) {
	var object Object
	err := s.do(ctx, call{
		Method:  http.MethodPost,
		URL:     s.uploadURL(o, "media"),
		Headers: map[string]string{"Content-Type": o.contentType()},
		Body:    data,
		Out:     &object,
	})
	if err != nil {
		return nil, fmt.Errorf("gcp: uploading %s/%s: %w", o.Bucket, o.Name, err)
	}
	return &object, nil
}

func (s *StorageClient) startResumable(ctx context.Context, o WriteObjectOptions) (string, error) {
	resp, err := s.send(ctx, call{
		Method:  http.MethodPost,
		URL:     s.uploadURL(o, "resumable"),
		Headers: map[string]string{"X-Upload-Content-Type": o.contentType()},
		Body:    map[string]string{"name": o.Name, "contentType": o.contentType()},
	})
	if err != nil {
		return "", fmt.Errorf("gcp: starting upload of %s/%s: %w", o.Bucket, o.Name, err)
	}
	defer resp.Body.Close()
	session := resp.Header.Get("Location")
	if session == "" {
		return "", fmt.Errorf("gcp: starting upload of %s/%s: response has no session Location", o.Bucket, o.Name)
	}
	return session, nil
}

func (s *StorageClient) uploadResumable(ctx context.Context, o WriteObjectOptions, first []byte) (*Object, error) {
	session, err := s.startResumable(ctx, o)
	if err != nil {
		return nil, err
	}
	rest := bufio.NewReader(o.Body)
	chunk, offset := first, int64(0)
	for {
		_, peekErr := rest.Peek(1)
		last := errors.Is(peekErr, io.EOF)
		if peekErr != nil && !last {
			return nil, fmt.Errorf("gcp: reading upload body for %s: %w", o.Name, peekErr)
		}
		object, err := s.putChunk(ctx, session, chunk, offset, last)
		if err != nil || last {
			return object, err
		}
		offset += int64(len(chunk))
		n, readErr := io.ReadFull(rest, first)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("gcp: reading upload body for %s: %w", o.Name, readErr)
		}
		chunk = first[:n]
	}
}

// putChunk sends one chunk of a resumable session. Every chunk but the last is
// acknowledged with 308; the last returns the created object.
func (s *StorageClient) putChunk(ctx context.Context, session string, chunk []byte, offset int64, last bool) (*Object, error) {
	total := "*"
	if last {
		total = fmt.Sprint(offset + int64(len(chunk)))
	}
	rangeHeader := fmt.Sprintf("bytes %d-%d/%s", offset, offset+int64(len(chunk))-1, total)
	resp, err := s.send(ctx, call{
		Method:       http.MethodPut,
		URL:          session,
		Headers:      map[string]string{"Content-Range": rangeHeader},
		Body:         chunk,
		AcceptStatus: []int{http.StatusPermanentRedirect},
	})
	if err != nil {
		return nil, fmt.Errorf("gcp: uploading chunk %s: %w", rangeHeader, err)
	}
	defer resp.Body.Close()
	if !last {
		return nil, nil
	}
	var object Object
	if err := json.NewDecoder(resp.Body).Decode(&object); err != nil {
		return nil, fmt.Errorf("gcp: decoding upload result: %w", err)
	}
	return &object, nil
}
