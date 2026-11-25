package gcp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/gcp"
)

var _ = Describe("Storage client", func() {
	var (
		ctx = context.Background()
		api *fakeAPI
	)

	AfterEach(func() { api.Close() })

	newClient := func(endpoint string) *gcp.StorageClient {
		client, err := gcp.NewStorageClient(gcp.Options{Endpoint: endpoint, TokenSource: staticToken(testToken)})
		Expect(err).NotTo(HaveOccurred())
		return client
	}

	It("lists objects under a prefix across pages with a bearer token", func() {
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Query().Get("pageToken") == "" {
				writeJSON(w, 200, `{"items":[{"name":"logs/a.txt","size":"5","updated":"2024-05-06T07:08:09.123Z"}],"nextPageToken":"p2"}`)
				return
			}
			writeJSON(w, 200, `{"items":[{"name":"logs/b.txt","size":"12","contentType":"text/plain"}]}`)
		})

		objects, err := newClient(api.URL).ListObjects(ctx, "bkt", "logs/")

		Expect(err).NotTo(HaveOccurred())
		Expect(objects).To(HaveLen(2))
		Expect(objects[0].Name).To(Equal("logs/a.txt"))
		Expect(objects[0].Size).To(Equal(int64(5)))
		Expect(objects[0].Updated.UTC().Format("2006-01-02T15:04:05.000Z")).To(Equal("2024-05-06T07:08:09.123Z"))
		Expect(objects[1].Size).To(Equal(int64(12)))
		requests := api.Requests()
		Expect(requests).To(HaveLen(2))
		Expect(requests[0].Path).To(Equal("/storage/v1/b/bkt/o"))
		Expect(requests[0].Query["prefix"]).To(ConsistOf("logs/"))
		Expect(requests[1].Query["pageToken"]).To(ConsistOf("p2"))
		Expect(requests[0].Header.Get("Authorization")).To(Equal("Bearer " + testToken))
	})

	It("accepts an endpoint that already carries /storage/v1/", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, 200, `{}`) })

		_, err := newClient(api.URL+"/storage/v1/").ListObjects(ctx, "bkt", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(api.Requests()[0].Path).To(Equal("/storage/v1/b/bkt/o"))
	})

	It("reads object metadata with the name path-escaped", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 200, `{"name":"dir/file one.txt","size":"3"}`)
		})

		object, err := newClient(api.URL).GetObject(ctx, "bkt", "dir/file one.txt")

		Expect(err).NotTo(HaveOccurred())
		Expect(object.Name).To(Equal("dir/file one.txt"))
		Expect(api.Requests()[0].Path).To(Equal("/storage/v1/b/bkt/o/dir%2Ffile%20one.txt"))
	})

	It("maps a missing object to a typed not-found error that matches fs.ErrNotExist", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 404, `{"error":{"code":404,"message":"No such object: bkt/x","status":"NOT_FOUND","errors":[{"reason":"notFound"}]}}`)
		})

		_, err := newClient(api.URL).GetObject(ctx, "bkt", "x")

		Expect(gcp.IsNotFound(err)).To(BeTrue())
		Expect(err).To(MatchError(fs.ErrNotExist))
		var apiErr *gcp.APIError
		Expect(err).To(BeAssignableToTypeOf(apiErr))
		apiErr = err.(*gcp.APIError)
		Expect(apiErr.StatusCode).To(Equal(404))
		Expect(apiErr.Status).To(Equal("NOT_FOUND"))
		Expect(apiErr.Reason).To(Equal("notFound"))
		Expect(apiErr.Message).To(Equal("No such object: bkt/x"))
	})

	It("streams the object body from alt=media", func() {
		payload := strings.Repeat("0123456789", 1000)
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = io.WriteString(w, payload) })

		reader, err := newClient(api.URL).OpenObject(ctx, "bkt", "a/b.bin")
		Expect(err).NotTo(HaveOccurred())
		defer reader.Close()
		data, err := io.ReadAll(reader)

		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(payload))
		request := api.Requests()[0]
		Expect(request.Path).To(Equal("/storage/v1/b/bkt/o/a%2Fb.bin"))
		Expect(request.Query["alt"]).To(ConsistOf("media"))
	})

	It("returns the error envelope when opening a missing object", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 404, `{"error":{"code":404,"message":"gone"}}`)
		})

		_, err := newClient(api.URL).OpenObject(ctx, "bkt", "x")

		Expect(gcp.IsNotFound(err)).To(BeTrue())
	})

	It("uploads a small body with a single media request", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeJSON(w, 200, `{"name":"a/b.txt","size":"5"}`)
		})

		object, err := newClient(api.URL).WriteObject(ctx, gcp.WriteObjectOptions{
			Bucket: "bkt", Name: "a/b.txt", Body: strings.NewReader("hello"), ContentType: "text/plain",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(object.Size).To(Equal(int64(5)))
		requests := api.Requests()
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Method).To(Equal(http.MethodPost))
		Expect(requests[0].Path).To(Equal("/upload/storage/v1/b/bkt/o"))
		Expect(requests[0].Query["uploadType"]).To(ConsistOf("media"))
		Expect(requests[0].Query["name"]).To(ConsistOf("a/b.txt"))
		Expect(requests[0].Header.Get("Content-Type")).To(Equal("text/plain"))
		Expect(string(requests[0].Body)).To(Equal("hello"))
	})

	It("streams a body larger than one chunk through a resumable session", func() {
		const chunk = 256 * 1024
		payload := bytes.Repeat([]byte("x"), 2*chunk+100)
		var session string
		api = newFakeAPI(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			switch r.Method {
			case http.MethodPost:
				w.Header().Set("Location", session)
				w.WriteHeader(200)
			default:
				if strings.HasSuffix(r.Header.Get("Content-Range"), fmt.Sprintf("/%d", len(payload))) {
					writeJSON(w, 200, fmt.Sprintf(`{"name":"big.bin","size":"%d"}`, len(payload)))
					return
				}
				w.WriteHeader(308)
			}
		})
		session = api.URL + "/upload/session/abc"

		object, err := newClient(api.URL).WriteObject(ctx, gcp.WriteObjectOptions{
			Bucket: "bkt", Name: "big.bin", Body: bytes.NewReader(payload), ChunkSize: chunk,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(object.Size).To(Equal(int64(len(payload))))
		requests := api.Requests()
		Expect(requests).To(HaveLen(4))
		Expect(requests[0].Query["uploadType"]).To(ConsistOf("resumable"))
		Expect(requests[1].Header.Get("Content-Range")).To(Equal(fmt.Sprintf("bytes 0-%d/*", chunk-1)))
		Expect(requests[2].Header.Get("Content-Range")).To(Equal(fmt.Sprintf("bytes %d-%d/*", chunk, 2*chunk-1)))
		Expect(requests[3].Header.Get("Content-Range")).To(Equal(fmt.Sprintf("bytes %d-%d/%d", 2*chunk, len(payload)-1, len(payload))))
		var sent []byte
		for _, request := range requests[1:] {
			Expect(request.Method).To(Equal(http.MethodPut))
			sent = append(sent, request.Body...)
		}
		Expect(sent).To(Equal(payload))
	})

	It("deletes an object", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(204) })

		Expect(newClient(api.URL).DeleteObject(ctx, "bkt", "a/b.txt")).To(Succeed())

		request := api.Requests()[0]
		Expect(request.Method).To(Equal(http.MethodDelete))
		Expect(request.Path).To(Equal("/storage/v1/b/bkt/o/a%2Fb.txt"))
	})

	It("falls back to STORAGE_EMULATOR_HOST when no endpoint is configured and sends no token without a source", func() {
		api = newFakeAPI(func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, 200, `{}`) })
		GinkgoT().Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(api.URL, "http://"))
		client, err := gcp.NewStorageClient(gcp.Options{})
		Expect(err).NotTo(HaveOccurred())

		_, err = client.ListObjects(ctx, "bkt", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(api.Requests()[0].Header.Get("Authorization")).To(BeEmpty())
	})

	It("rejects a malformed endpoint instead of guessing", func() {
		api = newFakeAPI(func(http.ResponseWriter, *http.Request, []byte) {})

		_, err := gcp.NewStorageClient(gcp.Options{Endpoint: "not a url"})

		Expect(err).To(HaveOccurred())
	})
})
