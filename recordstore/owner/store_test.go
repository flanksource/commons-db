// Specs for a Store held by more than one opener: one owns and writes the
// file, the others read it and hand their writes over the spool, and the
// next to take the lock when the owner closes takes over.
package owner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/owner"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/spool"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("Store", func() {
	var (
		ctx  context.Context
		path string
	)

	options := func(build string) owner.Options[*sqlite.Backend] {
		return owner.Options[*sqlite.Backend]{
			Path: path, Build: build, Store: sqlite.VersionedPath(path), CatalogVersion: sqlite.CatalogVersion,
			StartWait: 5 * time.Second, Poll: 100 * time.Millisecond,
			Open: func(_ context.Context, readOnly bool, submit recordstore.Submitter) (*sqlite.Backend, error) {
				return sqlite.Open(sqlite.Options{
					Path: path, Schema: recordstoretest.Schema, TTL: recordstoretest.TTL, SweepInterval: time.Hour,
					ReadOnly: readOnly, Submit: submit,
				})
			},
		}
	}
	openStore := func(options owner.Options[*sqlite.Backend]) *owner.Store[*sqlite.Backend] {
		store, err := owner.OpenUnshared(ctx, options)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(store.Close)
		return store
	}
	backendOf := func(store *owner.Store[*sqlite.Backend]) *sqlite.Backend {
		backend, err := store.Backend()
		Expect(err).ToNot(HaveOccurred())
		return backend
	}
	// names are the names of stream's rows; a stream not written yet holds
	// none, so a spec can poll for rows still being ingested.
	names := func(backend recordstore.Backend, stream string) []string {
		var names []string
		err := backend.Scan(ctx, stream, 0, func(_ int64, row recordstore.Row) error {
			names = append(names, row["name"].(string))
			return nil
		})
		if !errors.Is(err, recordstore.ErrNotFound) {
			Expect(err).ToNot(HaveOccurred())
		}
		return names
	}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
	})

	It("elects the first opener to own the store and publishes where to reach it", func() {
		first := openStore(options("build-1"))
		second := openStore(options("build-1"))

		Expect(first.Role()).To(Equal(owner.RoleOwner))
		Expect(second.Role()).To(Equal(owner.RoleReader))
		state, found, err := owner.ReadState(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(state.Phase).To(Equal(owner.PhaseReady))
		Expect(state.PID).To(Equal(os.Getpid()))
		Expect(state.Store).To(Equal(sqlite.VersionedPath(path)))
		Expect(state.CatalogVersion).To(Equal(sqlite.CatalogVersion))
		Expect(state.Socket).To(Equal(owner.SocketPath(path)))
		Expect(state.Spool).ToNot(BeNil())
		Expect(state.Spool.ManifestFormat).To(Equal(spool.ManifestFormat))
		Expect(state.Spool.Formats).To(ContainElement(spool.FormatNDJSON))
		status, err := second.Status()
		Expect(err).ToNot(HaveOccurred())
		Expect(status.Instance).To(Equal(state.Instance))
	})

	It("hands a reader's writes to the owner and returns the owner's exact results", func() {
		first := openStore(options("build-1"))
		second := openStore(options("build-1"))
		reader := backendOf(second)

		result, err := reader.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 1, To: 2}))
		Expect(names(backendOf(first), "run-1")).To(Equal([]string{"row-001", "row-002"}))
		Expect(names(reader, "run-1")).To(Equal([]string{"row-001", "row-002"}))

		Expect(reader.Seal(ctx, "run-1")).To(Succeed())
		_, err = backendOf(first).Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(errors.Is(err, recordstore.ErrSealed)).To(BeTrue())
		incoming, err := os.ReadDir(filepath.Join(path+".spool", "incoming"))
		Expect(err).ToNot(HaveOccurred())
		Expect(incoming).To(BeEmpty(), "ingested batches leave incoming")
	})

	It("leaves a write durable and pending when the caller stops waiting, to be awaited later", func() {
		openStore(options("build-1"))
		second := openStore(options("build-1"))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		_, err := backendOf(second).Append(cancelled, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(errors.Is(err, owner.ErrPending)).To(BeTrue(), "Append: %v", err)
		var pending *owner.PendingError
		Expect(errors.As(err, &pending)).To(BeTrue())

		result, err := second.Await(ctx, pending.BatchID)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries).To(HaveLen(1))
		Expect(result.Entries[0].Append.Window).To(Equal(recordstore.Window{From: 1, To: 1}))
	})

	It("promotes a reader when the owner closes, telling it its new role", func() {
		first := openStore(options("build-1"))
		second := openStore(options("build-2"))
		roles := make(chan owner.Role, 1)
		second.OnRole(func(role owner.Role) { roles <- role })
		_, err := backendOf(second).Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())

		Expect(first.Close()).To(Succeed())
		Eventually(roles, 5*time.Second).Should(Receive(Equal(owner.RoleOwner)))
		Expect(second.Role()).To(Equal(owner.RoleOwner))
		state, _, err := owner.ReadState(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(state.Build).To(Equal("build-2"))

		result, err := backendOf(second).Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(2, 2))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 2, To: 2}))
	})

	It("writes in bulk through a Writer as a reader and as the owner", func() {
		first := openStore(options("build-1"))
		second := openStore(options("build-1"))
		for _, store := range []*owner.Store[*sqlite.Backend]{second, first} {
			writer, err := store.Writer(recordstoretest.Schema, spool.WriterOptions{MaxRows: 2})
			Expect(err).ToNot(HaveOccurred())
			Expect(writer.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))).To(Succeed())
			Expect(writer.Seal(ctx, "run-1")).To(Succeed())
			Expect(writer.Flush(ctx, owner.FlushOptions{Wait: true})).To(Succeed())
			Expect(names(backendOf(first), "run-1")).To(Equal([]string{"row-001", "row-002", "row-003"}))
			meta, err := backendOf(first).Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(meta.Sealed).To(BeTrue())
			Expect(backendOf(first).Delete(ctx, "run-1")).To(Succeed())
		}
	})

	It("reports a bulk write the owner refused once it is flushed with a wait", func() {
		first := openStore(options("build-1"))
		second := openStore(options("build-1"))
		Expect(backendOf(first).Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).Error().ToNot(HaveOccurred())
		Expect(backendOf(first).Seal(ctx, "run-1")).To(Succeed())

		writer, err := second.Writer(recordstoretest.Schema, spool.WriterOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(writer.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(2, 2))).To(Succeed())
		err = writer.Flush(ctx, owner.FlushOptions{Wait: true})
		Expect(errors.Is(err, recordstore.ErrSealed)).To(BeTrue(), "Flush: %v", err)
	})

	It("moves a batch it cannot read to failed, recording why", func() {
		openStore(options("build-1"))
		dir, err := spool.OpenDir(path + ".spool")
		Expect(err).ToNot(HaveOccurred())
		staged := filepath.Join(path+".spool", "tmp", "foreign")
		Expect(os.MkdirAll(staged, 0o700)).To(Succeed())
		manifest, err := json.Marshal(map[string]any{"format": 2, "id": "b-future", "producer": map[string]any{"instance": "future"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(staged, "manifest.json"), manifest, 0o600)).To(Succeed())
		Expect(os.Rename(staged, filepath.Join(path+".spool", "incoming", "00000000000000000001-future-000000000001-b-future"))).To(Succeed())

		Eventually(func() ([]string, error) { return dir.Incoming() }, 5*time.Second).Should(BeEmpty())
		reason, err := os.ReadFile(filepath.Join(path+".spool", "failed", "00000000000000000001-future-000000000001-b-future", "error.json"))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(reason)).To(ContainSubstring("manifest format 2"))
	})

	It("applies one producer's batches in its order, whatever order they were published in", func() {
		first := openStore(options("build-1"))
		dir, err := spool.OpenDir(path + ".spool")
		Expect(err).ToNot(HaveOccurred())
		schema, err := recordstoretest.Schema(recordstoretest.Kind)
		Expect(err).ToNot(HaveOccurred())
		publish := func(seq int64, name string) {
			_, err := dir.Publish(recordstore.Batch{
				ID: name, Producer: recordstore.Producer{Instance: "cli-9", Seq: seq}, Schemas: []recordstore.KindSchema{schema},
				Entries: []recordstore.BatchEntry{{Op: recordstore.BatchAppend, Stream: "run-1", Kind: recordstoretest.Kind, Rows: []recordstore.Row{{"name": name}}}},
			}, spool.FormatNDJSON)
			Expect(err).ToNot(HaveOccurred())
		}
		publish(1, "a")
		Eventually(func() []string { return names(backendOf(first), "run-1") }, 5*time.Second).Should(Equal([]string{"a"}))
		publish(3, "c")
		Consistently(func() []string { return names(backendOf(first), "run-1") }, 500*time.Millisecond).Should(Equal([]string{"a"}))
		publish(2, "b")
		Eventually(func() []string { return names(backendOf(first), "run-1") }, 5*time.Second).Should(Equal([]string{"a", "b", "c"}))
	})

	It("shares one store between the openers of one path within a process", func() {
		first, err := owner.Open(ctx, options("build-1"))
		Expect(err).ToNot(HaveOccurred())
		second, err := owner.Open(ctx, options("build-1"))
		Expect(err).ToNot(HaveOccurred())
		Expect(second).To(BeIdenticalTo(first))

		Expect(first.Close()).To(Succeed())
		Expect(backendOf(second).Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).Error().ToNot(HaveOccurred())
		Expect(second.Close()).To(Succeed())
		_, found, err := owner.ReadState(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse(), "the last close releases the store")
	})

	It("spools a reader's writes to an owner of another catalog version, refusing its reads", func() {
		newer := options("build-new")
		newer.CatalogVersion = sqlite.CatalogVersion + 1
		first := openStore(options("build-1"))
		second := openStore(newer)

		_, err := second.Backend()
		Expect(errors.Is(err, owner.ErrCatalogVersion)).To(BeTrue(), "Backend: %v", err)
		writer, err := second.Writer(recordstoretest.Schema, spool.WriterOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(writer.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).To(Succeed())
		Expect(writer.Flush(ctx, owner.FlushOptions{})).To(Succeed())
		Eventually(func() []string { return names(backendOf(first), "run-1") }, 5*time.Second).Should(Equal([]string{"row-001"}))
	})
})
