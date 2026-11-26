package connection_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flanksource/deps/start"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/connection"
	dbctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
)

const (
	fakeGCSService = "fake-gcs-server"
	fakeGCSBucket  = "commons-db-it"
)

// skipWithoutDocker follows the repo's integration convention: a suite that
// needs infrastructure the machine does not have is skipped, not failed. Once
// docker answers, every later failure is a real failure.
func skipWithoutDocker() {
	if _, err := exec.LookPath("docker"); err != nil {
		Skip("docker is not installed; fake-gcs-server needs it")
	}
	probe := exec.Command("docker", "info", "--format", "{{.ServerVersion}}")
	probe.WaitDelay = 15 * time.Second
	if err := probe.Run(); err != nil {
		Skip("docker is not reachable (" + err.Error() + "); fake-gcs-server needs it")
	}
}

func createFakeGCSBucket(endpoint string) {
	body, err := json.Marshal(map[string]string{"name": fakeGCSBucket})
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.Post(endpoint+"/storage/v1/b?project=test", "application/json", bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
}

var _ = Describe("GCS filesystem against fake-gcs-server", Label("integration"), Ordered, func() {
	var (
		conn     connection.GCSConnection
		ctx      dbctx.Context
		endpoint string
	)

	BeforeAll(func(specCtx SpecContext) {
		skipWithoutDocker()
		instances, err := start.StartServices(specCtx, []start.ServiceRequest{
			{Name: fakeGCSService, Options: []start.Option{start.WithRuntime(start.RuntimeDocker), start.WithVolumeMode(start.VolumeEphemeral)}},
		})
		DeferCleanup(func(cleanupCtx SpecContext) {
			Expect(start.StopServices(cleanupCtx, instances)).To(Succeed())
		})
		Expect(err).NotTo(HaveOccurred())

		endpoint = instances[fakeGCSService].Connection.URL
		createFakeGCSBucket(endpoint)
		conn = connection.GCSConnection{GCPConnection: connection.GCPConnection{Endpoint: endpoint}, Bucket: fakeGCSBucket}
		ctx = dbctx.NewContext(context.Background())
	})

	filesystem := func() fs.FilesystemRW {
		filesystem, err := conn.Filesystem(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(filesystem.Close)
		return filesystem
	}

	It("writes, stats, lists, reads and deletes an object", func(specCtx SpecContext) {
		files := filesystem()
		const key = "it/dir/hello.txt"

		info, err := files.Write(specCtx, key, strings.NewReader("hello gcs"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Size()).To(Equal(int64(len("hello gcs"))))

		stat, err := files.Stat(key)
		Expect(err).NotTo(HaveOccurred())
		Expect(stat.Name()).To(Equal(key))
		Expect(stat.Size()).To(Equal(int64(len("hello gcs"))))

		listed, err := files.ReadDir("it/dir/")
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, len(listed))
		for i, entry := range listed {
			names[i] = entry.Name()
		}
		Expect(names).To(ConsistOf(key))

		reader, err := files.Read(specCtx, key)
		Expect(err).NotTo(HaveOccurred())
		data, err := io.ReadAll(reader)
		Expect(reader.Close()).To(Succeed())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("hello gcs"))

		Expect(files.Delete(specCtx, key)).To(Succeed())
		_, err = files.Stat(key)
		Expect(err).To(MatchError(os.ErrNotExist))
		Expect(gcp.IsNotFound(err)).To(BeTrue())
		Expect(files.Delete(specCtx, key)).To(Succeed(), "deleting a missing object is not an error")
	})

	It("streams a body larger than one upload chunk through a resumable session", func(specCtx SpecContext) {
		files := filesystem()
		payload := bytes.Repeat([]byte("0123456789abcdef"), (gcp.DefaultUploadChunkSize+1<<20)/16)

		info, err := files.Write(specCtx, "it/large.bin", bytes.NewReader(payload))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Size()).To(Equal(int64(len(payload))))

		reader, err := files.Read(specCtx, "it/large.bin")
		Expect(err).NotTo(HaveOccurred())
		defer reader.Close()
		data, err := io.ReadAll(reader)
		Expect(err).NotTo(HaveOccurred())
		Expect(bytes.Equal(data, payload)).To(BeTrue())
	})

	It("uploads with a small chunk size and lists the result by prefix", func(specCtx SpecContext) {
		client, err := conn.Client(ctx)
		Expect(err).NotTo(HaveOccurred())
		payload := bytes.Repeat([]byte("z"), 600*1024)

		object, err := client.WriteObject(specCtx, gcp.WriteObjectOptions{
			Bucket: fakeGCSBucket, Name: "it/chunked/a.bin", Body: bytes.NewReader(payload), ChunkSize: 256 * 1024,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(object.Size).To(Equal(int64(len(payload))))

		objects, err := client.ListObjects(specCtx, fakeGCSBucket, "it/chunked/")
		Expect(err).NotTo(HaveOccurred())
		Expect(objects).To(HaveLen(1))
		Expect(objects[0].Name).To(Equal("it/chunked/a.bin"))
	})
})
